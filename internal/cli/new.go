package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
	"path/filepath"
	"strings"

	"github.com/cafaye/cafe/internal/agent"
	"github.com/cafaye/cafe/internal/box"
	"github.com/cafaye/cafe/internal/scaffold"
	"github.com/google/uuid"
)

// newOptions is the parsed flag state for `cafe new`.
//
// The first version of this command said the template catalog "lands with the
// generator packet"; this is that packet, and the catalog is real: an embedded
// one, in internal/scaffold.
type newOptions struct {
	template string
	name     string
	force    bool
}

func newNewCommand(deps shipDeps) *Command {
	opts := &newOptions{}
	c := &Command{
		Name:    "new",
		Summary: "scaffold a new cafaye service or app",
		Usage:   "cafe new [flags] <name>",
		LongHelp: "Scaffolds a project into <name>/ from the template catalog embedded in this\n" +
			"binary. The catalog is read from internal/scaffold: one directory per template,\n" +
			"one index naming them, so adding a template is adding a directory and a row\n" +
			"rather than a recompile of a table written beside the code.\n" +
			"\n" +
			"The scaffolded cafaye.yml is validated against core's manifest schema before\n" +
			"anything is written — the same check `cafe contract lint` runs — so a scaffolded\n" +
			"project cannot be one the contract rejects.\n" +
			"\n" +
			"<name> is the cafaye namespace name: lowercase kebab-case, 2-40 characters.\n" +
			"It is the directory the project is written into, and the name the manifest\n" +
			"carries unless -name overrides it (a repository whose directory is not its\n" +
			"service name is exactly that case).\n" +
			"\n" +
			"A second `cafe new` over an existing project is refused rather than clobbered:\n" +
			"the file in the way may be a manifest somebody wrote by hand, and -force is\n" +
			"the deliberate override.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.template, "template", scaffold.DefaultTemplate, "template to scaffold from")
			fs.StringVar(&opts.name, "name", "", "override the name of the project")
			fs.BoolVar(&opts.force, "force", false, "overwrite an existing project")
		},
	}
	c.Run = func(args []string, env *Env) error {
		deps, urlErr := deps.withDefaults()
		if urlErr != nil {
			return urlErr
		}
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}

		// Agent registration: if not logged in, register and ask human to claim.
		dir, _ := defaultConfigDir()
		email := readHumanSubject(dir)
		sessionToken := ""
		if dir != "" && email != "" {
			ring := box.FileRing{Dir: filepath.Join(dir, "keyring")}
			if t, err := ring.Get(agent.KeyringService, humanAccount(email)); err == nil {
				sessionToken = strings.TrimSpace(t)
			}
		}
		if sessionToken == "" {
			identityURL := os.Getenv("IDENTITY_URL")
			if identityURL == "" {
				identityURL = "https://cafaye.com"
			}
			regBody, _ := json.Marshal(map[string]any{
				"name":   args[0],
				"device": map[string]string{"host": "cli"},
			})
			regURL := strings.TrimRight(identityURL, "/") + "/v1/agents"
			regReq, err := http.NewRequest("POST", regURL, strings.NewReader(string(regBody)))
			if err == nil {
				regReq.Header.Set("Content-Type", "application/json")
				resp, err := deps.client.Do(regReq)
				if err == nil {
					defer resp.Body.Close()
					raw, _ := io.ReadAll(resp.Body)
					if resp.StatusCode == 201 {
						var reg struct {
							Agent struct {
								ID     string `json:"id"`
								Status string `json:"status"`
							} `json:"agent"`
							APIKey struct {
								Token string `json:"token"`
							} `json:"api_key"`
							Claim struct {
								URL string `json:"url"`
							} `json:"claim"`
						}
						json.Unmarshal(raw, &reg)
						_, _ = fmt.Fprintf(env.Stdout, "Agent registered: %s (status: %s)\n", reg.Agent.ID, reg.Agent.Status)
						_, _ = fmt.Fprintf(env.Stdout, "Claim URL: %s\n", reg.Claim.URL)
						_, _ = fmt.Fprintf(env.Stdout, "Open the claim URL in your browser to link this agent to your account.\n")
						_, _ = fmt.Fprintf(env.Stdout, "Waiting for claim (up to 5 minutes)...\n")

						// Poll for claim completion.
						bootstrapToken := reg.APIKey.Token
						claimed := false
						for i := 0; i < 150; i++ {
							time.Sleep(2 * time.Second)
							homeReq, _ := http.NewRequest("GET", strings.TrimRight(identityURL, "/")+"/v1/agents/home", nil)
							homeReq.Header.Set("Authorization", "Bearer "+bootstrapToken)
							homeResp, err := deps.client.Do(homeReq)
							if err != nil {
								continue
							}
							homeRaw, _ := io.ReadAll(homeResp.Body)
							homeResp.Body.Close()
							var home struct {
								Claimed bool `json:"claimed"`
							}
							json.Unmarshal(homeRaw, &home)
							if home.Claimed {
								claimed = true
								break
							}
						}
						if !claimed {
							_, _ = fmt.Fprintf(env.Stderr, "  Timed out waiting for claim. Run 'cafe new %s' again after claiming.\n", args[0])
							return nil
						}
						_, _ = fmt.Fprintf(env.Stdout, "  Agent claimed! Minting session token...\n")

						// Mint a token for ship access.
						mintBody, _ := json.Marshal(map[string]any{"name": "cafaye-cli"})
						mintReq, _ := http.NewRequest("POST", strings.TrimRight(identityURL, "/")+"/v1/agents/tokens", strings.NewReader(string(mintBody)))
						mintReq.Header.Set("Authorization", "Bearer "+bootstrapToken)
						mintReq.Header.Set("Content-Type", "application/json")
						mintResp, err := deps.client.Do(mintReq)
						if err != nil {
							_, _ = fmt.Fprintf(env.Stderr, "  Failed to mint token: %v\n", err)
							return nil
						}
						mintRaw, _ := io.ReadAll(mintResp.Body)
						mintResp.Body.Close()
						var minted struct {
							Token string `json:"token"`
						}
						json.Unmarshal(mintRaw, &minted)
						if minted.Token == "" {
							_, _ = fmt.Fprintf(env.Stderr, "  Failed to mint token. Run 'cafe login' to authenticate manually.\n")
							return nil
						}
						// Store the token in the keyring.
						if dir != "" {
							ring := box.FileRing{Dir: filepath.Join(dir, "keyring")}
							ring.Set(agent.KeyringService, "agent/"+reg.Agent.ID, minted.Token)
							writeHumanSubject(dir, args[0]+"@cafaye")
						}
						_, _ = fmt.Fprintf(env.Stdout, "  Authenticated. Proceeding...\n")
					}
				}
			}
			_, _ = fmt.Fprintf(env.Stdout, "Not logged in. Log in first:\n")
			_, _ = fmt.Fprintf(env.Stdout, "  cafe login --email <you@example.com> --password-stdin\n")
			_, _ = fmt.Fprintf(env.Stdout, "Then re-run: cafe new %s\n", args[0])
			return nil
		}

		out, err := scaffold.New(scaffold.Options{
			Template: opts.template,
			Name:     opts.name,
			Dir:      args[0],
			Force:    opts.force,
		})
		if err != nil {
			return fmt.Errorf("cafe new: %w", err)
		}
		printScaffolded(env.Stdout, "new", out)

		// Cloud bootstrap: create project on ship if configured.
		owner := os.Getenv("CAFAYE_OWNER_ID")
		if owner == "" {
			owner = os.Getenv("SHIP_OWNER_ID")
		}
		if owner != "" {
			ownerID, err := uuid.Parse(owner)
			if err == nil {
				_, _ = fmt.Fprintf(env.Stdout, "\nCreating cloud project...\n")
				req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/projects/",
					map[string]any{"name": args[0], "owner_id": ownerID.String()})
				if err == nil {
					body, err := shipDo(deps.client, req)
					if err == nil {
						var proj struct {
							ID string `json:"id"`
						}
						json.Unmarshal(body, &proj)
						if proj.ID != "" {
							_, _ = fmt.Fprintf(env.Stdout, "  Project created: %s\n", proj.ID)
							_, _ = fmt.Fprintf(env.Stdout, "  Ship URL: %s\n", deps.shipURL)
						}
					}
				}
			}
		} else {
			_, _ = fmt.Fprintf(env.Stdout, "\n  (set CAFAYE_OWNER_ID to also create a cloud project)\n")
		}

		// Billing check: if user has a paid plan, provision dev box.
		// Otherwise, print checkout URL.
		if owner != "" {
			_, _ = fmt.Fprintf(env.Stdout, "\nChecking billing status...\n")
			billReq, err := shipRequest("GET", deps.shipURL, "/v1/subscriptions", nil)
			hasPaid := false
			if err == nil {
				billBody, err := shipDo(deps.client, billReq)
				if err == nil {
					var subs []struct {
						Status string `json:"status"`
					}
					if json.Unmarshal(billBody, &subs) == nil {
						for _, sub := range subs {
							if sub.Status == "active" {
								hasPaid = true
								break
							}
						}
					}
				}
			}
			if hasPaid {
				_, _ = fmt.Fprintf(env.Stdout, "  Paid plan active. Provisioning dev box...\n")
				boxReq, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/devboxes", map[string]any{"name": args[0]})
				if err == nil {
					boxBody, err := shipDo(deps.client, boxReq)
					if err == nil {
						var box struct {
							ID    string `json:"id"`
							State string `json:"state"`
						}
						json.Unmarshal(boxBody, &box)
						if box.ID != "" {
							_, _ = fmt.Fprintf(env.Stdout, "  Dev box: %s (%s)\n", box.ID, box.State)
						}
					} else {
						_, _ = fmt.Fprintf(env.Stderr, "  Dev box provisioning failed: %v\n", err)
					}
				}
			} else {
				_, _ = fmt.Fprintf(env.Stdout, "  No active subscription.\n")
				_, _ = fmt.Fprintf(env.Stdout, "  Subscribe: https://cafaye.com/v1/plans\n")
				_, _ = fmt.Fprintf(env.Stdout, "  Then run: cafe new %s (again) to provision your dev box\n", args[0])
			}
		}
		return nil
	}
	return c
}
