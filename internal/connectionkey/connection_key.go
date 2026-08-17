package connectionkey

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const Prefix = "dp-link."

type Payload struct {
	ServerURL      string `json:"serverUrl"`
	ActivationCode string `json:"activationCode"`
}

func Build(serverURL, activationCode string) (string, error) {
	normalizedURL, err := NormalizeServerURL(serverURL)
	if err != nil {
		return "", err
	}
	code := strings.TrimSpace(activationCode)
	if code == "" {
		return "", errors.New("连接密钥无法生成")
	}
	raw, err := json.Marshal(Payload{ServerURL: normalizedURL, ActivationCode: code})
	if err != nil {
		return "", err
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func Parse(value string) (Payload, error) {
	key := strings.TrimSpace(value)
	if key == "" {
		return Payload{}, errors.New("请输入连接密钥")
	}
	if !strings.HasPrefix(key, Prefix) {
		return Payload{}, errors.New("请使用新的连接密钥")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, Prefix))
	if err != nil {
		return Payload{}, errors.New("连接密钥无法读取，请重新生成")
	}
	var payload Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Payload{}, errors.New("连接密钥无法读取，请重新生成")
	}
	serverURL, err := NormalizeServerURL(payload.ServerURL)
	if err != nil {
		return Payload{}, errors.New("连接密钥中的服务地址无效，请重新生成")
	}
	code := strings.TrimSpace(payload.ActivationCode)
	if code == "" {
		return Payload{}, errors.New("连接密钥无法使用，请重新生成")
	}
	return Payload{ServerURL: serverURL, ActivationCode: code}, nil
}

func NormalizeServerURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("服务地址必须是无路径的 HTTPS 地址")
	}
	return "https://" + parsed.Host, nil
}
