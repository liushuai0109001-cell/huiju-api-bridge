package licensing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

const Product = "huiju-api-bridge"

type Claims struct {
	Product     string `json:"product"`
	LicenseID   string `json:"license_id"`
	Customer    string `json:"customer"`
	MachineCode string `json:"machine_code"`
	IssuedAt    int64  `json:"issued_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

func MachineCode() (string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", fmt.Errorf("读取 Windows 机器标识失败: %w", err)
	}
	defer key.Close()
	guid, _, err := key.GetStringValue("MachineGuid")
	if err != nil || strings.TrimSpace(guid) == "" {
		return "", fmt.Errorf("读取 Windows MachineGuid 失败: %w", err)
	}
	hostname, _ := os.Hostname()
	digest := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(guid)) + "|" + strings.ToUpper(hostname) + "|" + Product))
	value := strings.ToUpper(hex.EncodeToString(digest[:10]))
	parts := make([]string, 0, 4)
	for i := 0; i < len(value); i += 5 {
		parts = append(parts, value[i:i+5])
	}
	return "HJ-" + strings.Join(parts, "-"), nil
}

func Sign(privateKey ed25519.PrivateKey, claims Claims) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("签发私钥长度无效")
	}
	if claims.Product == "" {
		claims.Product = Product
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(privateKey, payload)
	encoding := base64.RawURLEncoding
	return "HJ1." + encoding.EncodeToString(payload) + "." + encoding.EncodeToString(signature), nil
}

func Verify(code string, publicKey ed25519.PublicKey, machineCode string, now time.Time) (Claims, error) {
	var claims Claims
	parts := strings.Split(strings.TrimSpace(code), ".")
	if len(parts) != 3 || parts[0] != "HJ1" {
		return claims, errors.New("授权码格式无效")
	}
	encoding := base64.RawURLEncoding
	payload, err := encoding.DecodeString(parts[1])
	if err != nil {
		return claims, errors.New("授权码内容无效")
	}
	signature, err := encoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(publicKey, payload, signature) {
		return claims, errors.New("授权签名无效")
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, errors.New("授权信息无效")
	}
	if claims.Product != Product {
		return claims, errors.New("授权产品不匹配")
	}
	if !strings.EqualFold(strings.TrimSpace(claims.MachineCode), strings.TrimSpace(machineCode)) {
		return claims, errors.New("授权码与本机机器码不匹配")
	}
	if claims.ExpiresAt <= 0 || now.Unix() > claims.ExpiresAt {
		return claims, errors.New("授权已过期")
	}
	if claims.IssuedAt > now.Add(24*time.Hour).Unix() {
		return claims, errors.New("系统时间异常，授权尚未生效")
	}
	return claims, nil
}

func DecodePrivateKey(value string) (ed25519.PrivateKey, error) {
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(data) != ed25519.PrivateKeySize {
		return nil, errors.New("私钥文件无效")
	}
	return ed25519.PrivateKey(data), nil
}

func DecodePublicKey(value string) (ed25519.PublicKey, error) {
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(data) != ed25519.PublicKeySize {
		return nil, errors.New("客户端授权公钥无效")
	}
	return ed25519.PublicKey(data), nil
}
