package client

import (
	"context"
	"fmt"
	"net/url"
)

// ModelInfo is one daemon configuration. It names a credential variable and never carries the value.
type ModelInfo struct {
	Name          string `json:"name"`
	Adapter       string `json:"adapter"`
	Endpoint      string `json:"endpoint"`
	Model         string `json:"model"`
	InputUSDPerM  string `json:"input_usd_per_m"`
	OutputUSDPerM string `json:"output_usd_per_m"`
	CredentialEnv string `json:"credential_env"`
	CredentialSet bool   `json:"credential_set"`
	Loopback      bool   `json:"loopback"`
	Scripted      bool   `json:"scripted"`
	Valid         bool   `json:"valid"`
	Problem       string `json:"problem"`
	Selected      bool   `json:"selected"`
}

// ListModels returns the daemon catalog. It does not call a model.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var body struct {
		Configs []ModelInfo `json:"configs"`
		Error   string      `json:"error"`
	}
	if err := c.get(ctx, "/v1/models", &body, true); err != nil {
		return nil, err
	}
	if body.Error != "" {
		return nil, fmt.Errorf("%s", body.Error)
	}
	return body.Configs, nil
}

// SelectModel stores the daemon default. It does not call a model.
func (c *Client) SelectModel(ctx context.Context, name string) (ModelInfo, error) {
	var body struct {
		Config ModelInfo `json:"config"`
		Error  string    `json:"error"`
	}
	if err := c.post(ctx, "/v1/models/select", map[string]any{"name": name}, &body); err != nil {
		return ModelInfo{}, err
	}
	if body.Error != "" {
		return ModelInfo{}, fmt.Errorf("%s", body.Error)
	}
	return body.Config, nil
}

// SelectSessionModel stores the session preference. It does not create a run or call a model.
func (c *Client) SelectSessionModel(ctx context.Context, sessionID, name, repo string) (ModelInfo, error) {
	payload := map[string]any{"name": name}
	if repo != "" {
		payload["repo_path"] = repo
	}
	var body struct {
		Config ModelInfo `json:"config"`
		Error  string    `json:"error"`
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/model"
	if err := c.post(ctx, path, payload, &body); err != nil {
		return ModelInfo{}, err
	}
	if body.Error != "" {
		return ModelInfo{}, fmt.Errorf("%s", body.Error)
	}
	return body.Config, nil
}
