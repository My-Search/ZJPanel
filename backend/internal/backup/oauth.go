package backup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"zjpanel/backend/internal/model"
)

// 云盘 OAuth：面板不内置任何厂商应用凭据，由用户在自己的开发者后台创建 OAuth 应用，
// 填入客户端 ID / 密钥；面板只负责发起授权、接收回调、保存刷新令牌。
// 这样凭据归用户所有，也不违反 Google/Microsoft 对第三方分发密钥的限制。
const (
	googleAuthEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint = "https://oauth2.googleapis.com/token"
	googleDriveScope    = "https://www.googleapis.com/auth/drive.file"

	msAuthEndpoint  = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	msTokenEndpoint = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	msGraphScope    = "Files.ReadWrite offline_access"
)

// OAuthState 一次性授权状态：绑定发起时的通道、回调地址与回跳地址。
type OAuthState struct {
	Kind        string
	ReturnPath  string
	RedirectURI string
}

var (
	oauthStateMu sync.Mutex
	oauthStates  = map[string]OAuthState{}
	oauthExpiry  = map[string]time.Time{}
)

const oauthStateTTL = 15 * time.Minute

// newOAuthState 生成一次性 state（32 位十六进制）。
func newOAuthState(st OAuthState) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b)
	now := time.Now()
	oauthStateMu.Lock()
	for k, exp := range oauthExpiry { // 顺带清理过期项
		if now.After(exp) {
			delete(oauthExpiry, k)
			delete(oauthStates, k)
		}
	}
	oauthStates[key] = st
	oauthExpiry[key] = now.Add(oauthStateTTL)
	oauthStateMu.Unlock()
	return key, nil
}

// TakeOAuthState 取出并销毁 state（一次性，防重放）。
func TakeOAuthState(key string) (OAuthState, bool) {
	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	st, ok := oauthStates[key]
	if !ok {
		return OAuthState{}, false
	}
	if time.Now().After(oauthExpiry[key]) {
		delete(oauthStates, key)
		delete(oauthExpiry, key)
		return OAuthState{}, false
	}
	delete(oauthStates, key)
	delete(oauthExpiry, key)
	return st, true
}

// sanitizeReturnPath 限制回跳地址为站内绝对路径，避免开放重定向。
func sanitizeReturnPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || len(p) > 200 {
		return "/"
	}
	if strings.ContainsAny(p, "\r\n") {
		return "/"
	}
	return p
}

// sanitizeRedirectURI 校验前端传入的回调地址：必须是 http(s)、带主机、以本功能回调路径结尾。
// 用于反代后按浏览器地址（而非后端推测的 Host）生成 redirect_uri，避免厂商校验不一致。
func sanitizeRedirectURI(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 || strings.ContainsAny(s, "\r\n") {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	if !strings.HasSuffix(u.Path, "/api/backup/oauth/callback") {
		return ""
	}
	return s
}

// OAuthAuthorizeURL 生成授权地址（要求远程配置已保存且类型匹配）。
func (s *Service) OAuthAuthorizeURL(kind, redirectURI, returnPath string) (string, error) {
	rc := s.loadRemote()
	if rc.Kind != kind {
		return "", errors.New("请先保存远程存储配置，再发起授权")
	}
	if !rc.IsOAuth() {
		return "", errors.New("当前通道不是 OAuth 云盘")
	}
	if rc.ClientID == "" {
		return "", errors.New("请先填写并保存客户端 ID")
	}
	ru := sanitizeRedirectURI(redirectURI)
	if ru == "" {
		return "", errors.New("回调地址无效，请从设置页发起授权")
	}
	state, err := newOAuthState(OAuthState{
		Kind:        kind,
		ReturnPath:  sanitizeReturnPath(returnPath),
		RedirectURI: ru,
	})
	if err != nil {
		return "", err
	}
	v := url.Values{}
	v.Set("client_id", rc.ClientID)
	v.Set("redirect_uri", ru)
	v.Set("response_type", "code")
	v.Set("state", state)
	switch kind {
	case StorageGoogleDrive:
		v.Set("scope", googleDriveScope)
		v.Set("access_type", "offline") // 必须 offline 才会下发 refresh_token
		v.Set("prompt", "consent")
		v.Set("include_granted_scopes", "true")
		return googleAuthEndpoint + "?" + v.Encode(), nil
	case StorageOneDrive:
		v.Set("scope", msGraphScope)
		v.Set("prompt", "select_account")
		return msAuthEndpoint + "?" + v.Encode(), nil
	}
	return "", fmt.Errorf("不支持的云盘类型: %s", kind)
}

// oauthTokens 令牌端点响应。
type oauthTokens struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func tokenEndpoint(kind string) (string, error) {
	switch kind {
	case StorageGoogleDrive:
		return googleTokenEndpoint, nil
	case StorageOneDrive:
		return msTokenEndpoint, nil
	}
	return "", fmt.Errorf("不支持的云盘类型: %s", kind)
}

// postToken 调用令牌端点（授权码交换 / 刷新令牌共用）。
func postToken(kind string, form url.Values) (*oauthTokens, error) {
	endpoint, err := tokenEndpoint(kind)
	if err != nil {
		return nil, err
	}
	body := form.Encode()
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求令牌失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var tok oauthTokens
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, fmt.Errorf("令牌响应解析失败（状态 %d）", resp.StatusCode)
	}
	if tok.Error != "" {
		return nil, fmt.Errorf("%s: %s", tok.Error, tok.ErrorDescription)
	}
	if resp.StatusCode >= 300 || tok.AccessToken == "" {
		return nil, fmt.Errorf("令牌交换失败（状态 %d）: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return &tok, nil
}

// exchangeCode 用授权码换取令牌。
func exchangeCode(kind string, rc RemoteConfig, code, redirectURI string) (*oauthTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", rc.ClientID)
	form.Set("client_secret", rc.ClientSecret)
	form.Set("redirect_uri", redirectURI)
	return postToken(kind, form)
}

// fetchAccessToken 用刷新令牌换新的访问令牌（Provider 每次操作前调用）。
func fetchAccessToken(rc RemoteConfig) (string, error) {
	if rc.RefreshToken == "" {
		return "", errors.New("尚未完成授权，请在设置页点击「授权」")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rc.RefreshToken)
	form.Set("client_id", rc.ClientID)
	form.Set("client_secret", rc.ClientSecret)
	tok, err := postToken(rc.Kind, form)
	if err != nil {
		return "", fmt.Errorf("刷新访问令牌失败（授权可能已失效，请重新授权）: %w", err)
	}
	return tok.AccessToken, nil
}

// oauthAccount 查询授权账号用于展示（失败不影响授权结果）。
func oauthAccount(kind, accessToken string) string {
	client := &http.Client{Timeout: 20 * time.Second}
	var req *http.Request
	var err error
	switch kind {
	case StorageGoogleDrive:
		req, err = http.NewRequest(http.MethodGet, "https://www.googleapis.com/drive/v3/about?fields=user(displayName,emailAddress)", nil)
	case StorageOneDrive:
		req, err = http.NewRequest(http.MethodGet, "https://graph.microsoft.com/v1.0/me?$select=displayName,userPrincipalName,mail", nil)
	default:
		return ""
	}
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return ""
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if kind == StorageGoogleDrive {
		var v struct {
			User struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			} `json:"user"`
		}
		if json.Unmarshal(raw, &v) == nil {
			if v.User.EmailAddress != "" {
				return v.User.EmailAddress
			}
			return v.User.DisplayName
		}
		return ""
	}
	var v struct {
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
		Mail              string `json:"mail"`
	}
	if json.Unmarshal(raw, &v) == nil {
		if v.Mail != "" {
			return v.Mail
		}
		if v.UserPrincipalName != "" {
			return v.UserPrincipalName
		}
		return v.DisplayName
	}
	return ""
}

// CompleteOAuth 回调收尾：校验通道、换取令牌、落库刷新令牌与账号。
func (s *Service) CompleteOAuth(kind, code, redirectURI string) (string, error) {
	if code == "" {
		return "", errors.New("授权被取消或未返回授权码")
	}
	rc := s.loadRemote()
	if rc.Kind != kind {
		return "", errors.New("远程存储类型已变更，请重新发起授权")
	}
	if rc.ClientID == "" || rc.ClientSecret == "" {
		return "", errors.New("客户端 ID / 密钥缺失，请先在设置页保存")
	}
	tok, err := exchangeCode(kind, rc, code, redirectURI)
	if err != nil {
		return "", err
	}
	account := oauthAccount(kind, tok.AccessToken)
	prev := rc.RefreshToken
	if tok.RefreshToken != "" {
		rc.RefreshToken = tok.RefreshToken
	} else {
		// 个别场景重新授权不下发新的 refresh_token：沿用旧值。
		rc.RefreshToken = prev
	}
	if account != "" {
		rc.Account = account
	}
	if err := s.saveRemote(rc); err != nil {
		return "", err
	}
	return rc.Account, nil
}

// ClearOAuth 撤销授权：删除刷新令牌与账号（保留客户端凭据，便于重新授权）。
// 直接写库而不是走 saveRemote（后者把空值视为「沿用旧密文」）。
func (s *Service) ClearOAuth() error {
	raw := s.loadRemoteRaw()
	raw.RefreshToken = ""
	raw.Account = ""
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return s.db.Save(&model.Setting{Key: settingRemoteKey, Value: string(b)}).Error
}
