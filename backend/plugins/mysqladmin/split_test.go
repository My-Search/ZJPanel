package mysqladmin

import (
	"strings"
	"testing"
)

func TestSplitSQLFile(t *testing.T) {
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
		got := splitSQLFile(tc.src)
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

func TestSplitSQLFileMysqldumpHeader(t *testing.T) {
	src := `-- MySQL dump 10.13
--
/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40103 SET TIME_ZONE='+00:00' */;

CREATE TABLE ` + "`t`" + ` (
  ` + "`id`" + ` int NOT NULL
);
-- Dump completed`
	got := splitSQLFile(src)
	if len(got) != 3 {
		t.Fatalf("期望 3 条，得到 %d: %v", len(got), got)
	}
	if !strings.Contains(got[2], "CREATE TABLE") {
		t.Errorf("第 3 条应为 CREATE TABLE: %q", got[2])
	}
}
