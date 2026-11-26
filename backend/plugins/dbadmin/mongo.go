package dbadmin

// MongoDB：文档型数据库。
// 「库」= database；「表」= collection；文档浏览按 _id 定位做替换/删除，
// 控制台直接执行 JSON 命令（db.RunCommand），与 mongosh 的 db.runCommand 等价。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

func mongoAuthSource(c *Connection) string {
	if s := strings.TrimSpace(c.Database); s != "" {
		return s
	}
	return "admin"
}

func mongoURI(c *Connection, plainPwd string) string {
	u := &url.URL{Scheme: "mongodb", Host: net.JoinHostPort(c.Host, c.Port)}
	if plainPwd != "" {
		u.User = url.UserPassword(c.User, plainPwd)
	} else if strings.TrimSpace(c.User) != "" {
		u.User = url.User(c.User)
	}
	q := url.Values{}
	q.Set("authSource", mongoAuthSource(c))
	q.Set("connectTimeoutMS", "10000")
	q.Set("serverSelectionTimeoutMS", "10000")
	q.Set("appName", "ZJPanel")
	u.RawQuery = q.Encode()
	return u.String()
}

func openMongo(c *Connection, plainPwd string) (*mongo.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cli, err := mongo.Connect(options.Client().ApplyURI(mongoURI(c, plainPwd)))
	if err != nil {
		return nil, err
	}
	if err := cli.Ping(ctx, readpref.Primary()); err != nil {
		_ = cli.Disconnect(context.Background())
		return nil, err
	}
	return cli, nil
}

// mongoClient 请求级客户端句柄（用完即断，避免跨请求持有连接池）。
type mongoClient struct{ c *mongo.Client }

func (m *mongoClient) close() { _ = m.c.Disconnect(context.Background()) }

func (p *Plugin) mongoClient(ctx context.Context, c *Connection) (*mongoClient, error) {
	cli, err := openMongo(c, p.openPassword(c))
	if err != nil {
		return nil, err
	}
	return &mongoClient{c: cli}, nil
}

func mongoDatabases(ctx context.Context, cli *mongo.Client) ([]string, error) {
	names, err := cli.ListDatabaseNames(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func mongoCollections(ctx context.Context, cli *mongo.Client, dbName string) ([]TableInfo, error) {
	names, err := cli.Database(dbName).ListCollectionNames(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]TableInfo, 0, len(names))
	for _, n := range names {
		out = append(out, TableInfo{Name: n, Rows: nil, Kind: "collection"})
	}
	return out, nil
}

// mongoSchema 采样前 50 个文档推断字段类型，并读取集合索引。
func mongoSchema(ctx context.Context, cli *mongo.Client, dbName, collName string) (*SchemaInfo, error) {
	coll := cli.Database(dbName).Collection(collName)
	cur, err := coll.Find(ctx, bson.M{}, options.Find().SetLimit(50))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	docs := []bson.M{}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}

	typeSet := map[string]map[string]bool{}
	for _, d := range docs {
		for k, v := range d {
			if typeSet[k] == nil {
				typeSet[k] = map[string]bool{}
			}
			typeSet[k][mongoTypeName(v)] = true
		}
	}
	cols := make([]ColumnInfo, 0, len(typeSet))
	for k, ts := range typeSet {
		names := make([]string, 0, len(ts))
		for n := range ts {
			names = append(names, n)
		}
		sort.Strings(names)
		ci := ColumnInfo{Name: k, Type: strings.Join(names, " | "), Nullable: true}
		if k == "_id" {
			ci.Key = "PRI"
		}
		cols = append(cols, ci)
	}
	sort.Slice(cols, func(i, j int) bool {
		if cols[i].Name == "_id" {
			return true
		}
		if cols[j].Name == "_id" {
			return false
		}
		return cols[i].Name < cols[j].Name
	})

	indexes := []IndexInfo{}
	idxCur, err := coll.Indexes().List(ctx)
	if err == nil {
		defer idxCur.Close(ctx)
		var idxDocs []bson.M
		if err := idxCur.All(ctx, &idxDocs); err == nil {
			for _, d := range idxDocs {
				ix := IndexInfo{Name: fmt.Sprint(d["name"])}
				if u, ok := d["unique"].(bool); ok {
					ix.Unique = u
				}
				switch k := d["key"].(type) {
				case bson.D:
					parts := make([]string, 0, len(k))
					for _, e := range k {
						parts = append(parts, fmt.Sprintf("%s(%v)", e.Key, e.Value))
					}
					ix.Columns = strings.Join(parts, ", ")
				case bson.M:
					for kk, vv := range k {
						ix.Columns += fmt.Sprintf("%s(%v), ", kk, vv)
					}
					ix.Columns = strings.TrimSuffix(ix.Columns, ", ")
				}
				indexes = append(indexes, ix)
			}
		}
	}

	var b strings.Builder
	b.WriteString("// MongoDB 集合 " + dbName + "." + collName + "（结构按文档采样推断，非固定 schema）\n")
	b.WriteString("// 索引：\n")
	for _, ix := range indexes {
		b.WriteString(fmt.Sprintf("db.%s.createIndex({ %s }%s, { name: %q });\n",
			collName, ix.Columns, boolOpt(ix.Unique, ", unique: true"), ix.Name))
	}
	if len(indexes) == 0 {
		b.WriteString("// （无索引）\n")
	}
	if len(docs) > 0 {
		sample, _ := bson.MarshalExtJSONIndent(docs[0], false, false, "", "  ")
		b.WriteString("// 样本文档：\n")
		b.WriteString(string(sample))
	}
	return &SchemaInfo{Columns: cols, Indexes: indexes, CreateSQL: b.String()}, nil
}

func boolOpt(b bool, s string) string {
	if b {
		return s
	}
	return ""
}

func mongoTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case int32:
		return "int"
	case int64:
		return "long"
	case float64:
		return "double"
	case bool:
		return "bool"
	case time.Time, bson.DateTime:
		return "date"
	case bson.ObjectID:
		return "objectId"
	case bson.Decimal128:
		return "decimal"
	case bson.Binary:
		return "binData"
	case bson.M, bson.D:
		return "object"
	case bson.A, []any:
		return "array"
	}
	return fmt.Sprintf("%T", v)
}

// mongoCell 把 BSON 值转成网格可渲染的标量；嵌套结构用扩展 JSON 文本表示。
func mongoCell(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string, bool, int32, int64, float64:
		return t
	case bson.ObjectID:
		return t.Hex()
	case time.Time:
		return t.Format("2006-01-02 15:04:05")
	case bson.DateTime:
		return time.UnixMilli(int64(t)).Format("2006-01-02 15:04:05")
	case bson.Decimal128:
		return t.String()
	case bson.M, bson.D, bson.A:
		if b, err := bson.MarshalExtJSON(t, false, false); err == nil {
			return string(b)
		}
	}
	b, err := bson.MarshalExtJSON(v, false, false)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// mongoFilter 解析用户输入的 JSON 过滤条件（支持扩展 JSON，如 {"_id":{"$oid":"…"}}）。
func mongoFilter(raw string) (bson.M, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return bson.M{}, nil
	}
	var f bson.M
	if err := bson.UnmarshalExtJSON([]byte(raw), false, &f); err != nil {
		return nil, fmt.Errorf("过滤条件不是合法 JSON: %w", err)
	}
	return f, nil
}

func mongoRows(ctx context.Context, cli *mongo.Client, dbName, collName, where, order, dir string, page, size int) (cols []string, rows [][]any, raw []string, total int64, sqlText string, err error) {
	filter, err := mongoFilter(where)
	if err != nil {
		return nil, nil, nil, 0, "", err
	}
	coll := cli.Database(dbName).Collection(collName)
	if total, err = coll.CountDocuments(ctx, filter); err != nil {
		return nil, nil, nil, 0, "", err
	}
	opt := options.Find().SetSkip(int64((page - 1) * size)).SetLimit(int64(size))
	if order != "" {
		d := int32(1)
		if strings.EqualFold(dir, "DESC") {
			d = -1
		}
		opt = opt.SetSort(bson.D{{Key: order, Value: d}})
	}
	cur, err := coll.Find(ctx, filter, opt)
	if err != nil {
		return nil, nil, nil, 0, "", err
	}
	defer cur.Close(ctx)
	docs := []bson.M{}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, nil, nil, 0, "", err
	}

	cols, rows = mongoDocsTable(docs, &raw)
	sqlText = fmt.Sprintf("db.%s.find(%s)", collName, strings.TrimSpace(where))
	if order != "" {
		d := "1"
		if strings.EqualFold(dir, "DESC") {
			d = "-1"
		}
		sqlText += fmt.Sprintf(".sort({%s: %s})", order, d)
	}
	sqlText += fmt.Sprintf(".skip(%d).limit(%d)", (page-1)*size, size)
	return cols, rows, raw, total, sqlText, nil
}

// mongoDocsTable 文档集合 → 列并集 + 行值 + 原始扩展 JSON（供编辑回填）。
func mongoDocsTable(docs []bson.M, raw *[]string) ([]string, [][]any) {
	colSet := map[string]bool{}
	for _, d := range docs {
		for k := range d {
			colSet[k] = true
		}
	}
	cols := make([]string, 0, len(colSet))
	for k := range colSet {
		cols = append(cols, k)
	}
	sort.Slice(cols, func(i, j int) bool {
		if cols[i] == "_id" {
			return true
		}
		if cols[j] == "_id" {
			return false
		}
		return cols[i] < cols[j]
	})
	index := map[string]int{}
	for i, c := range cols {
		index[c] = i
	}
	rows := make([][]any, 0, len(docs))
	for _, d := range docs {
		row := make([]any, len(cols))
		for i := range row {
			row[i] = nil
		}
		for k, v := range d {
			row[index[k]] = mongoCell(v)
		}
		rows = append(rows, row)
		if raw != nil {
			b, err := bson.MarshalExtJSON(d, false, false)
			if err != nil {
				b = []byte("{}")
			}
			*raw = append(*raw, string(b))
		}
	}
	return cols, rows
}

func mongoInsert(ctx context.Context, cli *mongo.Client, dbName, collName string, values map[string]any) error {
	doc, err := mongoDocFromAny(values)
	if err != nil {
		return err
	}
	_, err = cli.Database(dbName).Collection(collName).InsertOne(ctx, doc)
	return err
}

func mongoReplace(ctx context.Context, cli *mongo.Client, dbName, collName string, key, values map[string]any) error {
	filter, err := mongoDocFromAny(key)
	if err != nil {
		return err
	}
	doc, err := mongoDocFromAny(values)
	if err != nil {
		return err
	}
	_, err = cli.Database(dbName).Collection(collName).ReplaceOne(ctx, filter, doc)
	return err
}

func mongoDelete(ctx context.Context, cli *mongo.Client, dbName, collName string, key map[string]any) (int64, error) {
	filter, err := mongoDocFromAny(key)
	if err != nil {
		return 0, err
	}
	res, err := cli.Database(dbName).Collection(collName).DeleteOne(ctx, filter)
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func mongoDocFromAny(v map[string]any) (bson.M, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc bson.M
	if err := bson.UnmarshalExtJSON(b, false, &doc); err != nil {
		return nil, fmt.Errorf("不是合法文档 JSON: %w", err)
	}
	return doc, nil
}

func mongoDropCollection(ctx context.Context, cli *mongo.Client, dbName, collName string) error {
	return cli.Database(dbName).Collection(collName).Drop(ctx)
}

func mongoClearCollection(ctx context.Context, cli *mongo.Client, dbName, collName string) (int64, error) {
	res, err := cli.Database(dbName).Collection(collName).DeleteMany(ctx, bson.M{})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func mongoDropDatabase(ctx context.Context, cli *mongo.Client, dbName string) error {
	return cli.Database(dbName).Drop(ctx)
}

// mongoCommand 执行 JSON 形式的数据库命令（如 {"find":"users","filter":{},"limit":10}）。
func mongoCommand(ctx context.Context, cli *mongo.Client, dbName, raw string) ([]string, [][]any, error) {
	var cmd bson.D
	if err := bson.UnmarshalExtJSON([]byte(strings.TrimSpace(raw)), false, &cmd); err != nil {
		return nil, nil, fmt.Errorf("命令必须是 JSON 对象: %w", err)
	}
	var out bson.M
	if err := cli.Database(dbName).RunCommand(ctx, cmd).Decode(&out); err != nil {
		return nil, nil, err
	}
	if cur, ok := out["cursor"].(bson.M); ok {
		if batch, ok := cur["firstBatch"].(bson.A); ok {
			docs := make([]bson.M, 0, len(batch))
			for _, it := range batch {
				if m, ok := it.(bson.M); ok {
					docs = append(docs, m)
				}
			}
			cols, rows := mongoDocsTable(docs, nil)
			return cols, rows, nil
		}
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cols := keys
	row := make([]any, len(keys))
	for i, k := range keys {
		row[i] = mongoCell(out[k])
	}
	return cols, [][]any{row}, nil
}
