// Copyright 2016 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package executor_test

import (
	"context"

	. "github.com/pingcap/check"
	"github.com/pingcap/errors"
	"github.com/pingcap/parser/auth"
	"github.com/pingcap/parser/model"
	"github.com/pingcap/parser/mysql"
	"github.com/pingcap/parser/terror"
	"github.com/pingcap/tidb/config"
	"github.com/pingcap/tidb/domain"
	"github.com/pingcap/tidb/executor"
	"github.com/pingcap/tidb/planner/core"
	"github.com/pingcap/tidb/session"
	"github.com/pingcap/tidb/sessionctx"
	"github.com/pingcap/tidb/store/mockstore"
	"github.com/pingcap/tidb/store/mockstore/mocktikv"
	"github.com/pingcap/tidb/util/testkit"
	"github.com/pingcap/tidb/util/testutil"
)

func (s *testSuite3) TestCharsetDatabase(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	testSQL := `create database if not exists cd_test_utf8 CHARACTER SET utf8 COLLATE utf8_bin;`
	tk.MustExec(testSQL)

	testSQL = `create database if not exists cd_test_latin1 CHARACTER SET latin1 COLLATE latin1_swedish_ci;`
	tk.MustExec(testSQL)

	testSQL = `use cd_test_utf8;`
	tk.MustExec(testSQL)
	tk.MustQuery(`select @@character_set_database;`).Check(testkit.Rows("utf8"))
	tk.MustQuery(`select @@collation_database;`).Check(testkit.Rows("utf8_bin"))

	testSQL = `use cd_test_latin1;`
	tk.MustExec(testSQL)
	tk.MustQuery(`select @@character_set_database;`).Check(testkit.Rows("latin1"))
	tk.MustQuery(`select @@collation_database;`).Check(testkit.Rows("latin1_swedish_ci"))
}

func (s *testSuite3) TestDo(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("do 1, @a:=1")
	tk.MustQuery("select @a").Check(testkit.Rows("1"))
}

func (s *testSuite3) TestTransaction(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("begin")
	ctx := tk.Se.(sessionctx.Context)
	c.Assert(inTxn(ctx), IsTrue)
	tk.MustExec("commit")
	c.Assert(inTxn(ctx), IsFalse)
	tk.MustExec("begin")
	c.Assert(inTxn(ctx), IsTrue)
	tk.MustExec("rollback")
	c.Assert(inTxn(ctx), IsFalse)

	// Test that begin implicitly commits previous transaction.
	tk.MustExec("use test")
	tk.MustExec("create table txn (a int)")
	tk.MustExec("begin")
	tk.MustExec("insert txn values (1)")
	tk.MustExec("begin")
	tk.MustExec("rollback")
	tk.MustQuery("select * from txn").Check(testkit.Rows("1"))

	// Test that DDL implicitly commits previous transaction.
	tk.MustExec("begin")
	tk.MustExec("insert txn values (2)")
	tk.MustExec("create table txn2 (a int)")
	tk.MustExec("rollback")
	tk.MustQuery("select * from txn").Check(testkit.Rows("1", "2"))
}

func inTxn(ctx sessionctx.Context) bool {
	return (ctx.GetSessionVars().Status & mysql.ServerStatusInTrans) > 0
}

func (s *testSuite3) TestRole(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	// Make sure user test not in mysql.User.
	result := tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test" and Host="localhost"`)
	result.Check(nil)

	// Test for DROP ROLE.
	createRoleSQL := `CREATE ROLE 'test'@'localhost';`
	tk.MustExec(createRoleSQL)
	// Make sure user test in mysql.User.
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("")))
	// Insert relation into mysql.role_edges
	tk.MustExec("insert into mysql.role_edges (FROM_HOST,FROM_USER,TO_HOST,TO_USER) values ('localhost','test','%','root')")
	tk.MustExec("insert into mysql.role_edges (FROM_HOST,FROM_USER,TO_HOST,TO_USER) values ('localhost','test1','localhost','test1')")
	// Insert relation into mysql.default_roles
	tk.MustExec("insert into mysql.default_roles (HOST,USER,DEFAULT_ROLE_HOST,DEFAULT_ROLE_USER) values ('%','root','localhost','test')")
	tk.MustExec("insert into mysql.default_roles (HOST,USER,DEFAULT_ROLE_HOST,DEFAULT_ROLE_USER) values ('localhost','test','%','test1')")

	dropUserSQL := `DROP ROLE IF EXISTS 'test'@'localhost' ;`
	_, err := tk.Exec(dropUserSQL)
	c.Check(err, IsNil)

	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test" and Host="localhost"`)
	result.Check(nil)
	result = tk.MustQuery(`SELECT * FROM mysql.role_edges WHERE TO_USER="test" and TO_HOST="localhost"`)
	result.Check(nil)
	result = tk.MustQuery(`SELECT * FROM mysql.role_edges WHERE FROM_USER="test" and FROM_HOST="localhost"`)
	result.Check(nil)
	result = tk.MustQuery(`SELECT * FROM mysql.default_roles WHERE USER="test" and HOST="localhost"`)
	result.Check(nil)
	result = tk.MustQuery(`SELECT * FROM mysql.default_roles WHERE DEFAULT_ROLE_USER="test" and DEFAULT_ROLE_HOST="localhost"`)
	result.Check(nil)

	// Test for GRANT ROLE
	createRoleSQL = `CREATE ROLE 'r_1'@'localhost', 'r_2'@'localhost', 'r_3'@'localhost';`
	tk.MustExec(createRoleSQL)
	grantRoleSQL := `GRANT 'r_1'@'localhost' TO 'r_2'@'localhost';`
	tk.MustExec(grantRoleSQL)
	result = tk.MustQuery(`SELECT TO_USER FROM mysql.role_edges WHERE FROM_USER="r_1" and FROM_HOST="localhost"`)
	result.Check(testkit.Rows("r_2"))

	grantRoleSQL = `GRANT 'r_1'@'localhost' TO 'r_3'@'localhost', 'r_4'@'localhost';`
	_, err = tk.Exec(grantRoleSQL)
	c.Check(err, NotNil)

	// Test grant role for current_user();
	sessionVars := tk.Se.GetSessionVars()
	originUser := sessionVars.User
	sessionVars.User = &auth.UserIdentity{Username: "root", Hostname: "localhost", AuthUsername: "root", AuthHostname: "%"}
	tk.MustExec("grant 'r_1'@'localhost' to current_user();")
	tk.MustExec("revoke 'r_1'@'localhost' from 'root'@'%';")
	sessionVars.User = originUser

	result = tk.MustQuery(`SELECT FROM_USER FROM mysql.role_edges WHERE TO_USER="r_3" and TO_HOST="localhost"`)
	result.Check(nil)

	dropRoleSQL := `DROP ROLE IF EXISTS 'r_1'@'localhost' ;`
	tk.MustExec(dropRoleSQL)
	dropRoleSQL = `DROP ROLE IF EXISTS 'r_2'@'localhost' ;`
	tk.MustExec(dropRoleSQL)
	dropRoleSQL = `DROP ROLE IF EXISTS 'r_3'@'localhost' ;`
	tk.MustExec(dropRoleSQL)

	// Test for revoke role
	createRoleSQL = `CREATE ROLE 'test'@'localhost', r_1, r_2;`
	tk.MustExec(createRoleSQL)
	tk.MustExec("insert into mysql.role_edges (FROM_HOST,FROM_USER,TO_HOST,TO_USER) values ('localhost','test','%','root')")
	tk.MustExec("insert into mysql.role_edges (FROM_HOST,FROM_USER,TO_HOST,TO_USER) values ('%','r_1','%','root')")
	tk.MustExec("insert into mysql.role_edges (FROM_HOST,FROM_USER,TO_HOST,TO_USER) values ('%','r_2','%','root')")
	tk.MustExec("flush privileges")
	tk.MustExec("SET DEFAULT ROLE r_1, r_2 TO root")
	_, err = tk.Exec("revoke test@localhost, r_1 from root;")
	c.Check(err, IsNil)
	_, err = tk.Exec("revoke `r_2`@`%` from root, u_2;")
	c.Check(err, NotNil)
	_, err = tk.Exec("revoke `r_2`@`%` from root;")
	c.Check(err, IsNil)
	_, err = tk.Exec("revoke `r_1`@`%` from root;")
	c.Check(err, IsNil)
	result = tk.MustQuery(`SELECT * FROM mysql.default_roles WHERE DEFAULT_ROLE_USER="test" and DEFAULT_ROLE_HOST="localhost"`)
	result.Check(nil)
	result = tk.MustQuery(`SELECT * FROM mysql.default_roles WHERE USER="root" and HOST="%"`)
	result.Check(nil)
	dropRoleSQL = `DROP ROLE 'test'@'localhost', r_1, r_2;`
	tk.MustExec(dropRoleSQL)

	ctx := tk.Se.(sessionctx.Context)
	ctx.GetSessionVars().User = &auth.UserIdentity{Username: "test1", Hostname: "localhost"}
	c.Assert(tk.ExecToErr("SET ROLE role1, role2"), NotNil)
	tk.MustExec("SET ROLE ALL")
	tk.MustExec("SET ROLE ALL EXCEPT role1, role2")
	tk.MustExec("SET ROLE DEFAULT")
	tk.MustExec("SET ROLE NONE")
}

func (s *testSuite3) TestRoleAdmin(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("CREATE USER 'testRoleAdmin';")
	tk.MustExec("CREATE ROLE 'targetRole';")

	// Create a new session.
	se, err := session.CreateSession4Test(s.store)
	c.Check(err, IsNil)
	defer se.Close()
	c.Assert(se.Auth(&auth.UserIdentity{Username: "testRoleAdmin", Hostname: "localhost"}, nil, nil), IsTrue)

	ctx := context.Background()
	_, err = se.Execute(ctx, "GRANT `targetRole` TO `testRoleAdmin`;")
	c.Assert(err, NotNil)

	tk.MustExec("GRANT SUPER ON *.* TO `testRoleAdmin`;")
	_, err = se.Execute(ctx, "GRANT `targetRole` TO `testRoleAdmin`;")
	c.Assert(err, IsNil)
	_, err = se.Execute(ctx, "REVOKE `targetRole` FROM `testRoleAdmin`;")
	c.Assert(err, IsNil)

	tk.MustExec("DROP USER 'testRoleAdmin';")
	tk.MustExec("DROP ROLE 'targetRole';")
}

func (s *testSuite3) TestDefaultRole(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	createRoleSQL := `CREATE ROLE r_1, r_2, r_3, u_1;`
	tk.MustExec(createRoleSQL)

	tk.MustExec("insert into mysql.role_edges (FROM_HOST,FROM_USER,TO_HOST,TO_USER) values ('%','r_1','%','u_1')")
	tk.MustExec("insert into mysql.role_edges (FROM_HOST,FROM_USER,TO_HOST,TO_USER) values ('%','r_2','%','u_1')")

	tk.MustExec("flush privileges;")

	setRoleSQL := `SET DEFAULT ROLE r_3 TO u_1;`
	_, err := tk.Exec(setRoleSQL)
	c.Check(err, NotNil)

	setRoleSQL = `SET DEFAULT ROLE r_1 TO u_1000;`
	_, err = tk.Exec(setRoleSQL)
	c.Check(err, NotNil)

	setRoleSQL = `SET DEFAULT ROLE r_1, r_3 TO u_1;`
	_, err = tk.Exec(setRoleSQL)
	c.Check(err, NotNil)

	setRoleSQL = `SET DEFAULT ROLE r_1 TO u_1;`
	_, err = tk.Exec(setRoleSQL)
	c.Check(err, IsNil)
	result := tk.MustQuery(`SELECT DEFAULT_ROLE_USER FROM mysql.default_roles WHERE USER="u_1"`)
	result.Check(testkit.Rows("r_1"))
	setRoleSQL = `SET DEFAULT ROLE r_2 TO u_1;`
	_, err = tk.Exec(setRoleSQL)
	c.Check(err, IsNil)
	result = tk.MustQuery(`SELECT DEFAULT_ROLE_USER FROM mysql.default_roles WHERE USER="u_1"`)
	result.Check(testkit.Rows("r_2"))

	setRoleSQL = `SET DEFAULT ROLE ALL TO u_1;`
	_, err = tk.Exec(setRoleSQL)
	c.Check(err, IsNil)
	result = tk.MustQuery(`SELECT DEFAULT_ROLE_USER FROM mysql.default_roles WHERE USER="u_1"`)
	result.Check(testkit.Rows("r_1", "r_2"))

	setRoleSQL = `SET DEFAULT ROLE NONE TO u_1;`
	_, err = tk.Exec(setRoleSQL)
	c.Check(err, IsNil)
	result = tk.MustQuery(`SELECT DEFAULT_ROLE_USER FROM mysql.default_roles WHERE USER="u_1"`)
	result.Check(nil)

	dropRoleSQL := `DROP USER r_1, r_2, r_3, u_1;`
	tk.MustExec(dropRoleSQL)
}

func (s *testSuite3) TestUser(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	// Make sure user test not in mysql.User.
	result := tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test" and Host="localhost"`)
	result.Check(nil)
	// Create user test.
	createUserSQL := `CREATE USER 'test'@'localhost' IDENTIFIED BY '123';`
	tk.MustExec(createUserSQL)
	// Make sure user test in mysql.User.
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("123")))
	// Create duplicate user with IfNotExists will be success.
	createUserSQL = `CREATE USER IF NOT EXISTS 'test'@'localhost' IDENTIFIED BY '123';`
	tk.MustExec(createUserSQL)

	// Create duplicate user without IfNotExists will cause error.
	createUserSQL = `CREATE USER 'test'@'localhost' IDENTIFIED BY '123';`
	tk.MustGetErrCode(createUserSQL, mysql.ErrCannotUser)
	createUserSQL = `CREATE USER IF NOT EXISTS 'test'@'localhost' IDENTIFIED BY '123';`
	tk.MustExec(createUserSQL)
	tk.MustQuery("show warnings").Check(testutil.RowsWithSep("|", "Note|3163|User 'test'@'localhost' already exists."))
	dropUserSQL := `DROP USER IF EXISTS 'test'@'localhost' ;`
	tk.MustExec(dropUserSQL)
	// Create user test.
	createUserSQL = `CREATE USER 'test1'@'localhost';`
	tk.MustExec(createUserSQL)
	// Make sure user test in mysql.User.
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test1" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("")))
	dropUserSQL = `DROP USER IF EXISTS 'test1'@'localhost' ;`
	tk.MustExec(dropUserSQL)

	// Test alter user.
	createUserSQL = `CREATE USER 'test1'@'localhost' IDENTIFIED BY '123', 'test2'@'localhost' IDENTIFIED BY '123', 'test3'@'localhost' IDENTIFIED BY '123';`
	tk.MustExec(createUserSQL)
	alterUserSQL := `ALTER USER 'test1'@'localhost' IDENTIFIED BY '111';`
	tk.MustExec(alterUserSQL)
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test1" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("111")))
	alterUserSQL = `ALTER USER 'test_not_exist'@'localhost' IDENTIFIED BY '111';`
	tk.MustGetErrCode(alterUserSQL, mysql.ErrCannotUser)
	alterUserSQL = `ALTER USER 'test1'@'localhost' IDENTIFIED BY '222', 'test_not_exist'@'localhost' IDENTIFIED BY '111';`
	tk.MustGetErrCode(alterUserSQL, mysql.ErrCannotUser)
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test1" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("222")))

	alterUserSQL = `ALTER USER IF EXISTS 'test2'@'localhost' IDENTIFIED BY '222', 'test_not_exist'@'localhost' IDENTIFIED BY '1';`
	tk.MustExec(alterUserSQL)
	tk.MustQuery("show warnings").Check(testutil.RowsWithSep("|", "Note|3162|User 'test_not_exist'@'localhost' does not exist."))
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test2" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("222")))
	alterUserSQL = `ALTER USER IF EXISTS'test_not_exist'@'localhost' IDENTIFIED BY '1', 'test3'@'localhost' IDENTIFIED BY '333';`
	tk.MustExec(alterUserSQL)
	tk.MustQuery("show warnings").Check(testutil.RowsWithSep("|", "Note|3162|User 'test_not_exist'@'localhost' does not exist."))
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test3" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("333")))

	// Test alter user user().
	alterUserSQL = `ALTER USER USER() IDENTIFIED BY '1';`
	_, err := tk.Exec(alterUserSQL)
	c.Check(terror.ErrorEqual(err, errors.New("Session user is empty")), IsTrue, Commentf("err %v", err))
	tk.Se, err = session.CreateSession4Test(s.store)
	c.Check(err, IsNil)
	ctx := tk.Se.(sessionctx.Context)
	ctx.GetSessionVars().User = &auth.UserIdentity{Username: "test1", Hostname: "localhost"}
	tk.MustExec(alterUserSQL)
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="test1" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("1")))
	dropUserSQL = `DROP USER 'test1'@'localhost', 'test2'@'localhost', 'test3'@'localhost';`
	tk.MustExec(dropUserSQL)

	// Test drop user if exists.
	createUserSQL = `CREATE USER 'test1'@'localhost', 'test3'@'localhost';`
	tk.MustExec(createUserSQL)
	dropUserSQL = `DROP USER IF EXISTS 'test1'@'localhost', 'test2'@'localhost', 'test3'@'localhost' ;`
	tk.MustExec(dropUserSQL)
	tk.MustQuery("show warnings").Check(testutil.RowsWithSep("|", "Note|3162|User test2@localhost does not exist."))

	// Test negative cases without IF EXISTS.
	createUserSQL = `CREATE USER 'test1'@'localhost', 'test3'@'localhost';`
	tk.MustExec(createUserSQL)
	dropUserSQL = `DROP USER 'test1'@'localhost', 'test2'@'localhost', 'test3'@'localhost';`
	tk.MustGetErrCode(dropUserSQL, mysql.ErrCannotUser)
	dropUserSQL = `DROP USER 'test3'@'localhost';`
	tk.MustGetErrCode(dropUserSQL, mysql.ErrCannotUser)
	dropUserSQL = `DROP USER 'test1'@'localhost';`
	tk.MustGetErrCode(dropUserSQL, mysql.ErrCannotUser)
	// Test positive cases without IF EXISTS.
	createUserSQL = `CREATE USER 'test1'@'localhost', 'test3'@'localhost';`
	tk.MustExec(createUserSQL)
	dropUserSQL = `DROP USER 'test1'@'localhost', 'test3'@'localhost';`
	tk.MustExec(dropUserSQL)

	// Test 'identified by password'
	createUserSQL = `CREATE USER 'test1'@'localhost' identified by password 'xxx';`
	_, err = tk.Exec(createUserSQL)
	c.Assert(terror.ErrorEqual(executor.ErrPasswordFormat, err), IsTrue, Commentf("err %v", err))
	createUserSQL = `CREATE USER 'test1'@'localhost' identified by password '*3D56A309CD04FA2EEF181462E59011F075C89548';`
	tk.MustExec(createUserSQL)
	dropUserSQL = `DROP USER 'test1'@'localhost';`
	tk.MustExec(dropUserSQL)

	// Test drop user meet error
	_, err = tk.Exec(dropUserSQL)
	c.Assert(terror.ErrorEqual(err, executor.ErrCannotUser.GenWithStackByArgs("DROP USER", "")), IsTrue, Commentf("err %v", err))

	createUserSQL = `CREATE USER 'test1'@'localhost'`
	tk.MustExec(createUserSQL)
	createUserSQL = `CREATE USER 'test2'@'localhost'`
	tk.MustExec(createUserSQL)

	dropUserSQL = `DROP USER 'test1'@'localhost', 'test2'@'localhost', 'test3'@'localhost';`
	_, err = tk.Exec(dropUserSQL)
	c.Assert(terror.ErrorEqual(err, executor.ErrCannotUser.GenWithStackByArgs("DROP USER", "")), IsTrue, Commentf("err %v", err))
}

func (s *testSuite3) TestSetPwd(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	createUserSQL := `CREATE USER 'testpwd'@'localhost' IDENTIFIED BY '';`
	tk.MustExec(createUserSQL)
	result := tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="testpwd" and Host="localhost"`)
	result.Check(testkit.Rows(""))

	// set password for
	tk.MustExec(`SET PASSWORD FOR 'testpwd'@'localhost' = 'password';`)
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="testpwd" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("password")))

	// set password
	setPwdSQL := `SET PASSWORD = 'pwd'`
	// Session user is empty.
	_, err := tk.Exec(setPwdSQL)
	c.Check(err, NotNil)
	tk.Se, err = session.CreateSession4Test(s.store)
	c.Check(err, IsNil)
	ctx := tk.Se.(sessionctx.Context)
	ctx.GetSessionVars().User = &auth.UserIdentity{Username: "testpwd1", Hostname: "localhost", AuthUsername: "testpwd1", AuthHostname: "localhost"}
	// Session user doesn't exist.
	_, err = tk.Exec(setPwdSQL)
	c.Check(terror.ErrorEqual(err, executor.ErrPasswordNoMatch), IsTrue, Commentf("err %v", err))
	// normal
	ctx.GetSessionVars().User = &auth.UserIdentity{Username: "testpwd", Hostname: "localhost", AuthUsername: "testpwd", AuthHostname: "localhost"}
	tk.MustExec(setPwdSQL)
	result = tk.MustQuery(`SELECT Password FROM mysql.User WHERE User="testpwd" and Host="localhost"`)
	result.Check(testkit.Rows(auth.EncodePassword("pwd")))

}

func (s *testSuite3) TestKillStmt(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("kill 1")

	result := tk.MustQuery("show warnings")
	result.Check(testkit.Rows("Warning 1105 Invalid operation. Please use 'KILL TIDB [CONNECTION | QUERY] connectionID' instead"))
}

func (s *testSuite3) TestFlushPrivileges(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	tk.MustExec(`CREATE USER 'testflush'@'localhost' IDENTIFIED BY '';`)
	tk.MustExec(`FLUSH PRIVILEGES;`)
	tk.MustExec(`UPDATE mysql.User SET Select_priv='Y' WHERE User="testflush" and Host="localhost"`)

	// Create a new session.
	se, err := session.CreateSession4Test(s.store)
	c.Check(err, IsNil)
	defer se.Close()
	c.Assert(se.Auth(&auth.UserIdentity{Username: "testflush", Hostname: "localhost"}, nil, nil), IsTrue)

	ctx := context.Background()
	// Before flush.
	_, err = se.Execute(ctx, `SELECT Password FROM mysql.User WHERE User="testflush" and Host="localhost"`)
	c.Check(err, NotNil)

	tk.MustExec("FLUSH PRIVILEGES")

	// After flush.
	_, err = se.Execute(ctx, `SELECT Password FROM mysql.User WHERE User="testflush" and Host="localhost"`)
	c.Check(err, IsNil)

}

type testFlushSuite struct{}

func (s *testFlushSuite) TestFlushPrivilegesPanic(c *C) {
	// Run in a separate suite because this test need to set SkipGrantTable config.
	cluster := mocktikv.NewCluster()
	mocktikv.BootstrapWithSingleStore(cluster)
	mvccStore := mocktikv.MustNewMVCCStore()
	store, err := mockstore.NewMockTikvStore(
		mockstore.WithCluster(cluster),
		mockstore.WithMVCCStore(mvccStore),
	)
	c.Assert(err, IsNil)
	defer store.Close()

	saveConf := config.GetGlobalConfig()
	conf := *saveConf
	conf.Security.SkipGrantTable = true
	config.StoreGlobalConfig(&conf)

	dom, err := session.BootstrapSession(store)
	c.Assert(err, IsNil)
	defer dom.Close()

	tk := testkit.NewTestKit(c, store)
	tk.MustExec("FLUSH PRIVILEGES")

	config.StoreGlobalConfig(saveConf)
}

func (s *testSuite3) TestDropStats(c *C) {
	testKit := testkit.NewTestKit(c, s.store)
	testKit.MustExec("use test")
	testKit.MustExec("create table t (c1 int, c2 int)")
	do := domain.GetDomain(testKit.Se)
	is := do.InfoSchema()
	tbl, err := is.TableByName(model.NewCIStr("test"), model.NewCIStr("t"))
	c.Assert(err, IsNil)
	tableInfo := tbl.Meta()
	h := do.StatsHandle()
	h.Clear()
	testKit.MustExec("analyze table t")
	statsTbl := h.GetTableStats(tableInfo)
	c.Assert(statsTbl.Pseudo, IsFalse)

	testKit.MustExec("drop stats t")
	c.Assert(h.Update(is), IsNil)
	statsTbl = h.GetTableStats(tableInfo)
	c.Assert(statsTbl.Pseudo, IsTrue)

	testKit.MustExec("analyze table t")
	statsTbl = h.GetTableStats(tableInfo)
	c.Assert(statsTbl.Pseudo, IsFalse)

	h.SetLease(1)
	testKit.MustExec("drop stats t")
	c.Assert(h.Update(is), IsNil)
	statsTbl = h.GetTableStats(tableInfo)
	c.Assert(statsTbl.Pseudo, IsTrue)
	h.SetLease(0)
}

func (s *testSuite3) TestFlushTables(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	_, err := tk.Exec("FLUSH TABLES")
	c.Check(err, IsNil)

	_, err = tk.Exec("FLUSH TABLES WITH READ LOCK")
	c.Check(err, NotNil)

}

func (s *testSuite3) TestUseDB(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	_, err := tk.Exec("USE test")
	c.Check(err, IsNil)

	_, err = tk.Exec("USE ``")
	c.Assert(terror.ErrorEqual(core.ErrNoDB, err), IsTrue, Commentf("err %v", err))
}

func (s *testSuite3) TestStmtAutoNewTxn(c *C) {
	// Some statements are like DDL, they commit the previous txn automically.
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")

	// Fix issue https://github.com/pingcap/tidb/issues/10705
	tk.MustExec("begin")
	tk.MustExec("create user 'xxx'@'%';")
	tk.MustExec("grant all privileges on *.* to 'xxx'@'%';")

	tk.MustExec("create table auto_new (id int)")
	tk.MustExec("begin")
	tk.MustExec("insert into auto_new values (1)")
	tk.MustExec("revoke all privileges on *.* from 'xxx'@'%'")
	tk.MustExec("rollback") // insert statement has already committed
	tk.MustQuery("select * from auto_new").Check(testkit.Rows("1"))

	// Test the behavior when autocommit is false.
	tk.MustExec("set autocommit = 0")
	tk.MustExec("insert into auto_new values (2)")
	tk.MustExec("create user 'yyy'@'%'")
	tk.MustExec("rollback")
	tk.MustQuery("select * from auto_new").Check(testkit.Rows("1", "2"))

	tk.MustExec("drop user 'yyy'@'%'")
	tk.MustExec("insert into auto_new values (3)")
	tk.MustExec("rollback")
	tk.MustQuery("select * from auto_new").Check(testkit.Rows("1", "2"))
}

func (s *testSuite3) TestIssue9111(c *C) {
	// CREATE USER / DROP USER fails if admin doesn't have insert privilege on `mysql.user` table.
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("create user 'user_admin'@'localhost';")
	tk.MustExec("grant create user on *.* to 'user_admin'@'localhost';")

	// Create a new session.
	se, err := session.CreateSession4Test(s.store)
	c.Check(err, IsNil)
	defer se.Close()
	c.Assert(se.Auth(&auth.UserIdentity{Username: "user_admin", Hostname: "localhost"}, nil, nil), IsTrue)

	ctx := context.Background()
	_, err = se.Execute(ctx, `create user test_create_user`)
	c.Check(err, IsNil)
	_, err = se.Execute(ctx, `drop user test_create_user`)
	c.Check(err, IsNil)

	tk.MustExec("revoke create user on *.* from 'user_admin'@'localhost';")
	tk.MustExec("grant insert, delete on mysql.User to 'user_admin'@'localhost';")

	_, err = se.Execute(ctx, `flush privileges`)
	c.Check(err, IsNil)
	_, err = se.Execute(ctx, `create user test_create_user`)
	c.Check(err, IsNil)
	_, err = se.Execute(ctx, `drop user test_create_user`)
	c.Check(err, IsNil)

	_, err = se.Execute(ctx, `create role test_create_user`)
	c.Check(err, IsNil)
	_, err = se.Execute(ctx, `drop role test_create_user`)
	c.Check(err, IsNil)

	tk.MustExec("drop user 'user_admin'@'localhost';")
}
