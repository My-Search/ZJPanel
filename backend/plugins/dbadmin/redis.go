package dbadmin

// Redis：键值型，没有库/表概念。
// 「库」映射为 db0..dbN（SCAN 分页浏览键、查看值、删键、设置 TTL），
// 控制台直接执行 Redis 命令（支持引号与转义）。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const redisScanCount = 200

// redisClientHandle 请求级 Redis 客户端句柄。
type redisClientHandle struct{ cli *redis.Client }

func redisOptions(c *Connection, db int, plainPwd string) *redis.Options {
	return &redis.Options{
		Addr:         c.Host + ":" + c.Port,
		Username:     c.User,
		Password:     plainPwd,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolSize:     4,
		MaxRetries:   1,
	}
}

func openRedis(c *Connection, db int, plainPwd string) (*redis.Client, error) {
	cli := redis.NewClient(redisOptions(c, db, plainPwd))
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		_ = cli.Close()
		return nil, err
	}
	return cli, nil
}

func redisDBIndex(s string) int {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "db")
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 255 {
		return 0
	}
	return n
}

// redisDatabaseList 读取服务端 databases 配置（无权限时退回 16）。
func (p *Plugin) redisDatabaseList(ctx context.Context, c *Connection, plainPwd string) ([]string, error) {
	cli, err := openRedis(c, 0, plainPwd)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	n := 16
	if cfg, err := cli.ConfigGet(ctx, "databases").Result(); err == nil {
		if v, ok := cfg["databases"]; ok {
			if x, e := strconv.Atoi(v); e == nil && x > 0 && x <= 256 {
				n = x
			}
		}
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("db%d", i))
	}
	return out, nil
}

func redisTTLSeconds(d time.Duration) int64 {
	if d < 0 {
		return int64(d) // -1 永久 / -2 不存在（go-redis 以秒精度返回时与 -1s/-2s 对齐）
	}
	return int64(d / time.Second)
}

// redisScan 扫描一页键，并批量取 TYPE/TTL/MEMORY USAGE。
func redisScan(ctx context.Context, cli *redis.Client, pattern string, cursor uint64, count int64) ([]map[string]any, uint64, error) {
	if pattern == "" {
		pattern = "*"
	}
	keys, next, err := cli.Scan(ctx, cursor, pattern, count).Result()
	if err != nil {
		return nil, 0, err
	}
	pipe := cli.Pipeline()
	typeCmds := make([]*redis.StatusCmd, 0, len(keys))
	ttlCmds := make([]*redis.DurationCmd, 0, len(keys))
	sizeCmds := make([]*redis.IntCmd, 0, len(keys))
	for _, k := range keys {
		typeCmds = append(typeCmds, pipe.Type(ctx, k))
		ttlCmds = append(ttlCmds, pipe.TTL(ctx, k))
		sizeCmds = append(sizeCmds, pipe.MemoryUsage(ctx, k, 0))
	}
	_, _ = pipe.Exec(ctx) // 单条命令失败（如 MEMORY USAGE 受限）不影响整页
	out := make([]map[string]any, 0, len(keys))
	for i, k := range keys {
		item := map[string]any{"key": k}
		if typeCmds[i].Err() == nil {
			item["type"] = typeCmds[i].Val()
		}
		if ttlCmds[i].Err() == nil {
			item["ttl"] = redisTTLSeconds(ttlCmds[i].Val())
		}
		if sizeCmds[i].Err() == nil {
			item["size"] = sizeCmds[i].Val()
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]["key"]) < fmt.Sprint(out[j]["key"]) })
	return out, next, nil
}

// redisKeyDetail 读取键的类型、TTL、长度与内容（大集合只取前 200 个元素）。
func redisKeyDetail(ctx context.Context, cli *redis.Client, key string) (map[string]any, error) {
	typ, err := cli.Type(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	res := map[string]any{"key": key, "type": typ, "items": []map[string]any{}, "truncated": false}
	if ttl, err := cli.TTL(ctx, key).Result(); err == nil {
		res["ttl"] = redisTTLSeconds(ttl)
	}
	if size, err := cli.MemoryUsage(ctx, key, 0).Result(); err == nil {
		res["size"] = size
	}
	items := []map[string]any{}
	switch typ {
	case "string":
		v, err := cli.Get(ctx, key).Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		shown := v
		if len(shown) > 10000 {
			shown = shown[:10000]
			res["truncated"] = true
		}
		items = append(items, map[string]any{"name": "(值)", "value": shown})
		res["length"] = len(v)
	case "hash":
		m, err := cli.HGetAll(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		res["length"] = len(m)
		fields := make([]string, 0, len(m))
		for f := range m {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for idx, f := range fields {
			if idx >= 200 {
				res["truncated"] = true
				break
			}
			items = append(items, map[string]any{"name": f, "value": m[f]})
		}
	case "list":
		if l, err := cli.LLen(ctx, key).Result(); err == nil {
			res["length"] = l
		}
		vals, err := cli.LRange(ctx, key, 0, 199).Result()
		if err != nil {
			return nil, err
		}
		for i, v := range vals {
			items = append(items, map[string]any{"name": strconv.Itoa(i), "value": v})
		}
		if l, _ := res["length"].(int64); l > 200 {
			res["truncated"] = true
		}
	case "set":
		if l, err := cli.SCard(ctx, key).Result(); err == nil {
			res["length"] = l
		}
		vals, _, err := cli.SScan(ctx, key, 0, "", 200).Result()
		if err != nil {
			return nil, err
		}
		for i, v := range vals {
			items = append(items, map[string]any{"name": strconv.Itoa(i), "value": v})
		}
		if l, _ := res["length"].(int64); l > 200 {
			res["truncated"] = true
		}
	case "zset":
		if l, err := cli.ZCard(ctx, key).Result(); err == nil {
			res["length"] = l
		}
		vals, err := cli.ZRangeWithScores(ctx, key, 0, 199).Result()
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			items = append(items, map[string]any{"name": fmt.Sprint(v.Member), "value": v.Score})
		}
		if l, _ := res["length"].(int64); l > 200 {
			res["truncated"] = true
		}
	case "stream":
		if l, err := cli.XLen(ctx, key).Result(); err == nil {
			res["length"] = l
		}
		vals, err := cli.XRangeN(ctx, key, "-", "+", 50).Result()
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			b, _ := json.Marshal(v.Values)
			items = append(items, map[string]any{"name": v.ID, "value": string(b)})
		}
		if l, _ := res["length"].(int64); l > 50 {
			res["truncated"] = true
		}
	default:
		res["note"] = "键不存在或类型暂不支持预览"
	}
	res["items"] = items
	return res, nil
}

// splitCommand 拆分 Redis 命令行（支持单/双引号与反斜杠转义）。
func splitCommand(line string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	started := false
	escape := false
	for _, r := range line {
		if escape {
			cur.WriteRune(r)
			started = true
			escape = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escape = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if isSpaceRune(r) {
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
			continue
		}
		cur.WriteRune(r)
		started = true
	}
	if escape {
		cur.WriteRune('\\')
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}

// redisExec 执行任意 Redis 命令，返回二维表格（数组逐项展开，嵌套结构转 JSON）。
func redisExec(ctx context.Context, cli *redis.Client, line string) (cols []string, rows [][]any, err error) {
	args := splitCommand(line)
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("命令为空")
	}
	v, err := cli.Do(ctx, strsToAny(args)...).Result()
	if err != nil {
		return nil, nil, err
	}
	cols, rows = redisReplyTable(v)
	return cols, rows, nil
}

func strsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func redisReplyTable(v any) (cols []string, rows [][]any) {
	switch t := v.(type) {
	case nil:
		return []string{"结果"}, [][]any{{nil}}
	case []any:
		cols = []string{"#", "值"}
		for i, e := range t {
			rows = append(rows, []any{strconv.Itoa(i + 1), redisValueText(e)})
		}
		if len(rows) == 0 {
			rows = [][]any{{"0", "(空数组)"}}
		}
		return cols, rows
	case map[string]any:
		cols = []string{"字段", "值"}
		for k, e := range t {
			rows = append(rows, []any{k, redisValueText(e)})
		}
		sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i][0]) < fmt.Sprint(rows[j][0]) })
		return cols, rows
	case map[string]string:
		cols = []string{"字段", "值"}
		for k, e := range t {
			rows = append(rows, []any{k, e})
		}
		sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i][0]) < fmt.Sprint(rows[j][0]) })
		return cols, rows
	default:
		return []string{"结果"}, [][]any{{redisValueText(v)}}
	}
}

func redisValueText(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string, int64, float64, bool:
		return t
	case []any, map[string]any:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return fmt.Sprint(v)
}

// redisStringValue 取键的字符串值（用于编辑弹窗回填，仅 string 类型）。
func redisStringValue(ctx context.Context, cli *redis.Client, key string) (string, error) {
	return cli.Get(ctx, key).Result()
}
