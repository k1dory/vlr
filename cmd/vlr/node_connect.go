package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/util"
)

// cmdNodeConnect attaches THIS data-plane node to a vlr-main-agent: it switches
// the node to the `child` role IN PLACE (keeping the Reality keys and users, so
// no link breaks), generates the heartbeat/pull tokens, points the node at the
// agent, and — when given the agent's admin token — registers the node on the
// agent in one shot.
//
// Flag-first, then interactive: run `vlr node connect` on a terminal and it asks
// for everything; pass flags to script it.
func cmdNodeConnect(args []string) error {
	fs := newFlagSet("node connect")
	cfgPath := fs.String("config", "", "config path")
	agent := fs.String("agent", "", "agent base URL, e.g. https://main.example:8443")
	agentToken := fs.String("agent-token", "", "agent AGENT_API_TOKEN (to auto-register; empty = print curl)")
	nodeURL := fs.String("node-url", "", "this node's API base reachable by the agent, e.g. https://node:9777")
	pullListen := fs.String("pull-listen", "", "bind for this node's API (default 0.0.0.0:9777 so the agent can reach it)")
	token := fs.String("token", "", "heartbeat token (auto-generated if empty)")
	pullBearer := fs.String("pull-bearer", "", "pull bearer the agent must present (auto-generated if empty)")
	region := fs.String("region", "", "region label, e.g. RU/Yandex")
	insecure := fs.Bool("insecure", false, "skip TLS verify when registering on the agent (lab only)")
	noRegister := fs.Bool("no-register", false, "don't call the agent; just configure the node")
	_ = fs.Parse(args)

	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}
	if c.Role == config.RoleMain {
		return fmt.Errorf("это main-узел; `vlr node connect` запускается на дочернем (RU) узле")
	}

	interactive := isInteractive()

	// --- agent URL ---
	if *agent == "" && interactive {
		*agent = ask("URL агента (напр. https://main.example:8443)", *agent)
	}
	if *agent == "" {
		return fmt.Errorf("--agent обязателен (или запусти `vlr node connect` в терминале)")
	}
	*agent = strings.TrimRight(*agent, "/")

	// --- this node's API URL the agent will call ---
	if *nodeURL == "" {
		def := ""
		if c.Entry.Host != "" {
			def = "https://" + c.Entry.Host + ":9777"
		}
		if interactive {
			*nodeURL = ask("Адрес API этого узла для агента (host:port базы)", def)
		} else {
			*nodeURL = def
		}
	}
	if *nodeURL == "" {
		return fmt.Errorf("--node-url обязателен (адрес :9777 этого узла, видимый агенту)")
	}
	*nodeURL = strings.TrimRight(*nodeURL, "/")

	// --- bind: default to a reachable address, not loopback ---
	if *pullListen == "" {
		def := "0.0.0.0:9777"
		if interactive {
			*pullListen = ask("Bind API узла (агент должен достучаться)", def)
		} else {
			*pullListen = def
		}
	}

	if *region == "" {
		if interactive {
			*region = ask("Регион (метка)", c.Region)
		} else if c.Region != "" {
			*region = c.Region
		}
	}

	// --- tokens: reuse existing child tokens, else generate ---
	if *token == "" {
		if c.Child.Token != "" {
			*token = c.Child.Token
		} else if *token, err = util.RandHex(24); err != nil {
			return err
		}
	}
	if *pullBearer == "" {
		if c.Child.PullBearer != "" {
			*pullBearer = c.Child.PullBearer
		} else if *pullBearer, err = util.RandHex(24); err != nil {
			return err
		}
	}
	// The node's user API token must exist for the agent to create/delete users.
	if c.APIToken == "" {
		if c.APIToken, err = util.RandHex(24); err != nil {
			return err
		}
	}

	// --- mutate role to child IN PLACE (keys/users/cascade preserved) ---
	c.Role = config.RoleChild
	c.Region = *region
	c.Child = config.ChildConfig{
		MainURL:          *agent + "/v1",
		Token:            *token,
		PullBearer:       *pullBearer,
		HeartbeatSeconds: 20,
		PullListen:       *pullListen,
	}
	if err := config.Save(resolveConfigPath(*cfgPath), c); err != nil {
		return err
	}
	fmt.Printf("\n✓ узел переведён в режим child (ключи и пользователи сохранены)\n")
	fmt.Printf("  main_url:     %s/v1\n", *agent)
	fmt.Printf("  pull_listen:  %s\n", *pullListen)

	// --- register on the agent (or print the curl) ---
	reg := nodeReg{
		NodeID: c.NodeID, Region: *region,
		PullURL: *nodeURL + "/v1/pull", PullBearer: *pullBearer,
		UserAPIURL: *nodeURL, UserAPIToken: c.APIToken,
		HeartbeatToken: *token,
	}
	if *noRegister {
		printRegisterCurl(*agent, reg)
		return finishConnect()
	}
	if *agentToken == "" && interactive {
		*agentToken = ask("AGENT_API_TOKEN для авто-регистрации (пусто = показать curl)", "")
	}
	if *agentToken == "" {
		printRegisterCurl(*agent, reg)
		return finishConnect()
	}
	if err := registerOnAgent(context.Background(), *agent, *agentToken, reg, *insecure); err != nil {
		fmt.Printf("\n⚠ авто-регистрация не удалась: %v\nЗарегистрируй вручную:\n", err)
		printRegisterCurl(*agent, reg)
		return finishConnect()
	}
	fmt.Printf("✓ узел зарегистрирован на агенте (%s)\n", c.NodeID)
	return finishConnect()
}

// nodeReg mirrors the agent's POST /v1/nodes body (store.NodeReg JSON).
type nodeReg struct {
	NodeID         string `json:"node_id"`
	PullURL        string `json:"pull_url"`
	PullBearer     string `json:"pull_bearer"`
	HeartbeatToken string `json:"heartbeat_token"`
	UserAPIURL     string `json:"user_api_url"`
	UserAPIToken   string `json:"user_api_token"`
	Region         string `json:"region"`
}

func registerOnAgent(ctx context.Context, agent, agentToken string, reg nodeReg, insecure bool) error {
	body, _ := json.Marshal(reg)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agent+"/v1/nodes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentToken)
	client := &http.Client{Timeout: 15 * time.Second}
	if insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("agent returned %d", resp.StatusCode)
	}
	return nil
}

func printRegisterCurl(agent string, reg nodeReg) {
	b, _ := json.Marshal(reg)
	fmt.Printf("\nЗарегистрируй узел на агенте (выполни на main или где есть AGENT_API_TOKEN):\n")
	fmt.Printf("  curl -XPOST %s/v1/nodes \\\n    -H \"Authorization: Bearer $AGENT_API_TOKEN\" \\\n    -d '%s'\n",
		agent, string(b))
}

func finishConnect() error {
	fmt.Println("\nдальше: применить и запустить демон:")
	fmt.Println("  vlr up                      # если Xray ещё не поднят")
	fmt.Println("  systemctl restart vlr       # перезапустить демон → пойдут heartbeat'ы")
	return nil
}
