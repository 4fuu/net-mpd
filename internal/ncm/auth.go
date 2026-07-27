package ncm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-musicfox/netease-music/service"
)

type QRLogin struct {
	Key string
	URL string
}

type QRStatus struct {
	Code    int
	Message string
}

func (c *Client) CookiePath() string { return c.cookiePath }

func (c *Client) Save() error {
	if err := c.jar.Save(); err != nil {
		return fmt.Errorf("save cookie jar: %w", err)
	}
	return nil
}

func (c *Client) BeginQRLogin() (QRLogin, error) {
	c.lockSDK()
	defer sdkMu.Unlock()
	svc := &service.LoginQRService{}
	code, body, loginURL, err := svc.GetKey()
	if err != nil {
		return QRLogin{}, fmt.Errorf("create QR login: %w", err)
	}
	if code != 200 || svc.UniKey == "" || loginURL == "" {
		return QRLogin{}, responseError("create QR login", code, body)
	}
	return QRLogin{Key: svc.UniKey, URL: loginURL}, nil
}

func (c *Client) CheckQRLogin(key string) (QRStatus, error) {
	if key == "" {
		return QRStatus{}, errors.New("QR login key is empty")
	}
	c.lockSDK()
	code, body, err := (&service.LoginQRService{UniKey: key}).CheckQR()
	sdkMu.Unlock()
	if err != nil {
		return QRStatus{}, fmt.Errorf("check QR login: %w", err)
	}
	status := QRStatus{Code: int(code), Message: responseMessage(body)}
	if status.Code == 803 {
		if err := c.Save(); err != nil {
			return QRStatus{}, err
		}
	}
	return status, nil
}

func (c *Client) RefreshLogin() error {
	c.lockSDK()
	code, body, err := (&service.LoginRefreshService{}).LoginRefresh()
	sdkMu.Unlock()
	if err != nil {
		return fmt.Errorf("refresh login: %w", err)
	}
	if code != 200 {
		return responseError("refresh login", code, body)
	}
	return c.Save()
}

// Logout attempts remote logout and always removes local credentials. Remote
// failure does not prevent local cleanup, but the two outcomes are reported
// separately so callers cannot mistake a failed local deletion for success.
func (c *Client) Logout() (remoteErr, localErr error) {
	c.lockSDK()
	code, body, remoteErr := (&service.LogoutService{}).Logout()
	sdkMu.Unlock()
	if remoteErr == nil && code != 200 {
		remoteErr = responseError("remote logout", code, body)
	}
	localErr = c.clearLocalCredentials()
	return remoteErr, localErr
}

func (c *Client) clearLocalCredentials() error {
	c.jar.RemoveAll()
	if err := os.Remove(c.cookiePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove local cookie jar: %w", err)
	}
	return nil
}

func (c *Client) ImportCookies(sourcePath string) error {
	if sourcePath == "" {
		return errors.New("source cookie path is empty")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return fmt.Errorf("open source cookie jar: %w", err)
	}

	temporary, err := os.CreateTemp(filepath.Dir(c.cookiePath), ".cookie-import-*")
	if err != nil {
		return fmt.Errorf("create temporary cookie jar: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary cookie jar: %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		temporary.Close()
		return fmt.Errorf("open source cookie jar: %w", err)
	}
	_, copyErr := io.Copy(temporary, source)
	closeSourceErr := source.Close()
	closeTemporaryErr := temporary.Close()
	if copyErr != nil {
		return fmt.Errorf("copy source cookie jar: %w", copyErr)
	}
	if closeSourceErr != nil {
		return fmt.Errorf("close source cookie jar: %w", closeSourceErr)
	}
	if closeTemporaryErr != nil {
		return fmt.Errorf("close temporary cookie jar: %w", closeTemporaryErr)
	}

	staged, err := Open(temporaryPath)
	if err != nil {
		return fmt.Errorf("load source cookie jar: %w", err)
	}
	if _, err := staged.Account(); err != nil {
		return fmt.Errorf("source cookies are not logged in: %w", err)
	}
	if err := staged.Save(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, c.cookiePath); err != nil {
		return fmt.Errorf("install imported cookie jar: %w", err)
	}
	reopened, err := Open(c.cookiePath)
	if err != nil {
		return fmt.Errorf("reopen imported cookie jar: %w", err)
	}
	c.jar = reopened.jar
	return nil
}

func responseMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Message != "" {
		return payload.Message
	}
	return payload.Msg
}

func responseError(op string, code float64, body []byte) error {
	message := responseMessage(body)
	if message == "" {
		return fmt.Errorf("%s: code %.0f", op, code)
	}
	return fmt.Errorf("%s: code %.0f: %s", op, code, message)
}
