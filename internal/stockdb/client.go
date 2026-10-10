package stockdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 表名常量。
const (
	TableStockCodes = "股票代码"
	TableBoards     = "板块*"
	TableDailyK     = "日k"
)

// DefaultLocalURL 是 free-stockdb 的默认本地地址。
const DefaultLocalURL = "http://127.0.0.1:7899"

// Client 访问 free-stockdb 的 HTTP 接口。
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient 创建客户端。baseURL 形如 http://127.0.0.1:7899。
func NewClient(baseURL string) *Client {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		trimmed = DefaultLocalURL
	}
	return &Client{
		baseURL: trimmed,
		httpClient: &http.Client{
			Timeout: 90 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: 128,
				MaxConnsPerHost:     128,
			},
		},
	}
}

// BaseURL 返回当前服务地址。
func (c *Client) BaseURL() string { return c.baseURL }

// 调用底层 http://host/?cmd=...&t=...&k1=...&k2=...
func (c *Client) call(ctx context.Context, cmd, table, k1, k2 string) (any, error) {
	body, err := c.callRaw(ctx, cmd, table, k1, k2)
	if err != nil {
		return nil, err
	}
	return Decode(body)
}

// callRaw 取回**未解码**的响应体。
//
// 日k 那几张"一条记录 21 个字段、一次要几百万条"的表不适合走 map[string]any
// 的通用解码（见 quotes.go），它们需要自己在原始字节上落位。
func (c *Client) callRaw(ctx context.Context, cmd, table, k1, k2 string) ([]byte, error) {
	endpoint := c.baseURL + "/?cmd=" + url.QueryEscape(cmd) + "&t=" + url.QueryEscape(table)
	if k1 != "" {
		endpoint += "&k1=" + url.QueryEscape(k1)
	}
	if k2 != "" {
		endpoint += "&k2=" + url.QueryEscape(k2)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/x-msgpack")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stockdb 返回 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// Get 对应 cmd=get。
func (c *Client) Get(ctx context.Context, table, k1, k2 string) (any, error) {
	return c.call(ctx, "get", table, k1, k2)
}

// Vals 对应 cmd=vals。
func (c *Client) Vals(ctx context.Context, table, k1, k2 string) (any, error) {
	return c.call(ctx, "vals", table, k1, k2)
}

// Keys 对应 cmd=keys。
func (c *Client) Keys(ctx context.Context, table, k1, k2 string) (any, error) {
	return c.call(ctx, "keys", table, k1, k2)
}

// Ping 探测服务是否可用，返回股票代码总数。
func (c *Client) Ping(ctx context.Context) (int, error) {
	codes, err := c.StockCodes(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, list := range codes {
		total += len(list)
	}
	return total, nil
}

// StockCodes 读取「股票代码」表：map[首位数字][]6 位代码。
func (c *Client) StockCodes(ctx context.Context) (map[string][]string, error) {
	raw, err := c.Get(ctx, TableStockCodes, "", "")
	if err != nil {
		return nil, err
	}
	buckets, ok := ToMap(raw)
	if !ok {
		// 缺失的键会返回空数组
		if list, isList := ToSlice(raw); isList && len(list) == 0 {
			return map[string][]string{}, nil
		}
		return nil, errors.New("stockdb: 股票代码表返回了非预期的结构")
	}

	out := make(map[string][]string, len(buckets))
	for bucket, value := range buckets {
		items, _ := ToSlice(value)
		list := make([]string, 0, len(items))
		for _, item := range items {
			if code := strings.TrimSpace(String(item)); code != "" {
				list = append(list, code)
			}
		}
		out[bucket] = list
	}
	return out, nil
}

// LatestTradeDate 通过若干只活跃股票的最新日 K 键推断最新交易日。
func (c *Client) LatestTradeDate(ctx context.Context, candidates ...string) (int, error) {
	if len(candidates) == 0 {
		candidates = []string{"000001", "600519"}
	}

	latest := 0
	var lastErr error
	for _, code := range candidates {
		raw, err := c.Keys(ctx, TableDailyK, "key:"+code, "N")
		if err != nil {
			lastErr = err
			continue
		}
		items, _ := ToSlice(raw)
		for _, item := range items {
			key := String(item)
			idx := strings.LastIndex(key, ":")
			if idx < 0 {
				continue
			}
			if n, convErr := strconv.Atoi(key[idx+1:]); convErr == nil && n > latest {
				latest = n
			}
		}
	}

	if latest == 0 {
		if lastErr != nil {
			return 0, lastErr
		}
		return 0, errors.New("stockdb: 无法确定最新交易日（日 K 数据可能尚未同步）")
	}
	return latest, nil
}

// QuotePoint 读取某只股票在指定交易日的日 K 记录，缺失时返回 nil。
func (c *Client) QuotePoint(ctx context.Context, code string, date int) (*Quote, error) {
	body, err := c.callRaw(ctx, "get", fmt.Sprintf("%s:%s:%d", TableDailyK, code, date), "", "")
	if err != nil {
		return nil, err
	}
	quotes, err := DecodeQuotes(body)
	if err != nil {
		return nil, err
	}
	if len(quotes) == 0 {
		return nil, nil
	}
	return quotes[0], nil
}

// QuoteRangeDesc 读取区间日 K，返回顺序为最新在前。
func (c *Client) QuoteRangeDesc(ctx context.Context, code string, start, end int) ([]*Quote, error) {
	body, err := c.callRaw(ctx, "vals", TableDailyK, "key:"+code, fmt.Sprintf("fwd:%d,%d", start, end))
	if err != nil {
		return nil, err
	}
	return DecodeQuotes(body)
}

// Boards 读取「板块」表，返回概念/申万各级行业板块。
func (c *Client) Boards(ctx context.Context) ([]Board, error) {
	raw, err := c.Get(ctx, TableBoards, "", "")
	if err != nil {
		return nil, err
	}
	items, _ := ToSlice(raw)
	out := make([]Board, 0, len(items))
	for _, item := range items {
		pair, ok := ToSlice(item)
		if !ok || len(pair) < 2 {
			continue
		}
		m, ok := ToMap(pair[1])
		if !ok {
			continue
		}
		out = append(out, BoardFromMap(m))
	}
	return out, nil
}
