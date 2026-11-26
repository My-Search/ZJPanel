package dbadmin

import (
	"strings"
	"testing"
)

func TestSplitMySQL(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"简单语句", "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"字符串内分号", "INSERT INTO t VALUES ('a;b', \"c;d\");", []string{"INSERT INTO t VALUES ('a;b', \"c;d\")"}},
		{"转义与双写引号", `INSERT INTO t VALUES ('it\'s', 'x''y');`, []string{`INSERT INTO t VALUES ('it\'s', 'x''y')`}},
		{"反引号内分号", "INSERT INTO `a;b` VALUES (1);", []string{"INSERT INTO `a;b` VALUES (1)"}},
		{"行注释", "-- c1\nSELECT 1; # c2\n", []string{"-- c1\nSELECT 1"}},
		{"块注释", "/* hi */ SELECT 1;", []string{"/* hi */ SELECT 1"}},
		{"版本化注释", "/*!40101 SET NAMES utf8 */;\nSELECT 2;", []string{"/*!40101 SET NAMES utf8 */", "SELECT 2"}},
		{"DELIMITER 触发器", "DELIMITER //\nCREATE TRIGGER tr BEFORE INSERT ON x FOR EACH ROW BEGIN SET NEW.a=1; END//\nDELIMITER ;\nSELECT 5;",
			[]string{"CREATE TRIGGER tr BEFORE INSERT ON x FOR EACH ROW BEGIN SET NEW.a=1; END", "SELECT 5"}},
		{"DELIMITER $$", "DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END$$\nDELIMITER ;\nCALL p();",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END", "CALL p()"}},
		{"纯注释跳过", "-- only comment\n/* block */\n", []string{}},
		{"无尾分号", "SELECT 1", []string{"SELECT 1"}},
		{"USE 与反引号库", "USE `a;b`;\nSELECT 1;", []string{"USE `a;b`", "SELECT 1"}},
		{"缩进 DELIMITER", "  DELIMITER  //\nSELECT 6//\nDELIMITER ;", []string{"SELECT 6"}},
	}
	for _, tc := range cases {
		got := splitStatements("mysql", tc.src)
		if len(got) != len(tc.want) {
			t.Errorf("%s: 得到 %d 条 %v，期望 %d 条", tc.name, len(got), got, len(tc.want))
			continue
		}
		for i := range got {
			if strings.TrimSpace(got[i]) != tc.want[i] {
				t.Errorf("%s: 语句 %d = %q，期望 %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestSplitMysqldumpHeader(t *testing.T) {
	src := "-- MySQL dump 10.13\n--\n/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;\n" +
		"/*!40103 SET TIME_ZONE='+00:00' */;\n\nCREATE TABLE `t` (\n  `id` int NOT NULL\n);\n-- Dump completed"
	got := splitStatements("mysql", src)
	if len(got) != 3 {
		t.Fatalf("期望 3 条，得到 %d: %v", len(got), got)
	}
	if !strings.Contains(got[2], "CREATE TABLE") {
		t.Errorf("第 3 条应为 CREATE TABLE: %q", got[2])
	}
}

func TestSplitPostgresDollarQuote(t *testing.T) {
	src := "CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql;\nSELECT 2;"
	got := splitStatements("postgres", src)
	if len(got) != 2 {
		t.Fatalf("期望 2 条，得到 %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "SELECT 1;") {
		t.Errorf("美元引用内分号不应切分: %q", got[0])
	}
}

func TestSplitSQLServerGO(t *testing.T) {
	src := "CREATE TABLE t (id int);\nGO\nINSERT INTO t VALUES (1);\nGO\n"
	got := splitStatements("sqlserver", src)
	if len(got) != 2 {
		t.Fatalf("期望 2 条，得到 %d: %v", len(got), got)
	}
}

func TestSplitOracleBlock(t *testing.T) {
	src := "BEGIN\n  UPDATE t SET a = 1;\n  COMMIT;\nEND;\n/\nSELECT 1 FROM dual;\n"
	got := splitStatements("oracle", src)
	if len(got) != 2 {
		t.Fatalf("期望 2 条，得到 %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "COMMIT;") {
		t.Errorf("PL/SQL 块内分号不应切分: %q", got[0])
	}
}

func TestDialectRegistry(t *testing.T) {
	for _, kind := range []string{"mysql", "postgres", "sqlite", "sqlserver", "oracle"} {
		d, err := sqlDialectFor(kind)
		if err != nil || d.Kind() != kind {
			t.Errorf("%s 方言缺失: %v", kind, err)
		}
		if !isSQLKind(kind) {
			t.Errorf("%s 应属于 SQL 类型", kind)
		}
	}
	if isSQLKind("redis") || isSQLKind("mongodb") {
		t.Error("redis/mongodb 不应归为 SQL 类型")
	}
	if _, err := sqlDialectFor("redis"); err == nil {
		t.Error("redis 不应有 SQL 方言")
	}
}

func TestConnValidation(t *testing.T) {
	if err := validateHostPort("127.0.0.1", "3306"); err != nil {
		t.Errorf("合法主机端口应通过: %v", err)
	}
	if err := validateHostPort("127.0.0.1", "3306)/mysql?allowAllFiles=true"); err == nil {
		t.Error("注入端口参数应被拒绝")
	}
	if err := validateHostPort("evil.com/mysql?allowAllFiles=true", "3306"); err == nil {
		t.Error("注入主机参数应被拒绝")
	}
	c := &Connection{Type: "mysql", Host: "127.0.0.1", User: "root"}
	c.normalize()
	if c.Port != "3306" {
		t.Errorf("默认端口应为 3306，得到 %q", c.Port)
	}
	if !validName("schema.table") || !validName("表_a-1") {
		t.Error("合法标识符应通过")
	}
	for _, bad := range []string{"", "a;b", "a`b", "a]b", "a/b"} {
		if validName(bad) {
			t.Errorf("非法标识符 %q 应被拒绝", bad)
		}
	}
	if err := (&Connection{Type: "sqlite", Path: "relative.db"}).validate(); err == nil {
		t.Error("SQLite 相对路径应被拒绝")
	}
}
