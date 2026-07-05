/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package guard

import "testing"

func TestClassifyOne(t *testing.T) {
	cases := []struct {
		sql     string
		want    StmtClass
		wantErr bool
	}{
		{"SELECT * FROM t1", ClassSelect, false},
		{"SELECT 1 UNION SELECT 2", ClassSelect, false},
		{"SHOW TABLES", ClassUtility, false},
		{"UPDATE t1 SET a = 1 WHERE id = 1", ClassUpdate, false},
		{"SELECT 1; SELECT 2", "", true}, // 多语句
		{"SELEKT 1", "", true},           // 解析失败
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			got, err := ClassifyOne(c.sql)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("class = %s, want %s", got, c.want)
			}
		})
	}
}
