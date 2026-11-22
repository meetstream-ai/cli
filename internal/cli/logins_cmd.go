package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// requestJSON keeps the same API client and error/output conventions as botAction.
func requestJSON(cmd *cobra.Command, c *api.Client, method, path string, body any) (any, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	var v any
	err := c.Do(cmd.Context(), method, path, r, &v)
	return v, err
}

func loginPassword(cmd *cobra.Command, stdin bool) (string, error) {
	var password string
	if stdin {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", usageErr("could not read password from stdin", "")
		}
		password = string(b)
		if strings.HasSuffix(password, "\n") {
			password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
		}
	} else if password = os.Getenv("MEETSTREAM_LOGIN_PASSWORD"); password == "" {
		input, ok := cmd.InOrStdin().(*os.File)
		if g.NoInput || !ok || !term.IsTerminal(int(input.Fd())) {
			return "", usageErr("no password provided", "use --password-stdin or MEETSTREAM_LOGIN_PASSWORD")
		}
		fmt.Fprint(os.Stderr, "Account password (input hidden): ")
		b, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", usageErr("could not read password", "use --password-stdin")
		}
		password = string(b)
	}
	if password == "" {
		return "", usageErr("password is empty", "")
	}
	return password, nil
}

func newLoginsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "logins", Short: "Manage signed-in bot domains and accounts", GroupID: "meet"}
	for _, p := range []string{"teams", "google"} {
		cmd.AddCommand(loginPlatform(p, false))
	}
	return cmd
}

func loginPlatform(platform string, hidden bool) *cobra.Command {
	cmd := &cobra.Command{Use: platform, Short: "Manage " + platform + " signed-in accounts", Hidden: hidden}
	if hidden {
		cmd.GroupID = "meet"
	}
	google := platform == "google"
	domains, logins := "/"+platform+"-login-domains", "/"+platform+"-logins"
	for _, action := range []string{"domains", "domain", "add-domain", "update-domain", "remove-domain", "list", "get", "add", "set-password", "set-cert", "enable", "disable", "remove"} {
		if google && (action == "get" || action == "set-password") || !google && action == "set-cert" {
			continue
		}
		var domain, email, name, mode, key, cert string
		var inactive, stdin, yes bool
		n, use := 0, action
		switch action {
		case "domain", "add-domain", "update-domain", "remove-domain":
			n = 1
			use += " <domain>"
		case "get", "set-password", "set-cert", "enable", "disable", "remove":
			n = 1
			use += " <login-id>"
		}
		sub := botAction(use, n, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
			method, path := "GET", domains
			var body map[string]any
			switch action {
			case "domain":
				path += "/" + url.PathEscape(args[0])
			case "add-domain", "update-domain":
				body = map[string]any{}
				if name != "" {
					body["name"] = name
				}
				if mode != "" {
					body["login_mode"] = mode
				}
				if action == "add-domain" {
					method = "POST"
					k := "domain"
					if google {
						k = "sso_workspace_domain"
					}
					body[k] = args[0]
					if !google {
						body["login_mode"] = "always"
					}
				} else {
					method = "PATCH"
					path += "/" + url.PathEscape(args[0])
					if len(body) == 0 {
						return nil, usageErr("nothing to update", "pass --name or --login-mode (Google)")
					}
				}
			case "remove-domain", "remove":
				ok, err := confirmDelete(cmd, yes)
				if err != nil {
					return nil, err
				}
				if !ok {
					return map[string]any{"deleted": false}, nil
				}
				method = "DELETE"
				if action == "remove" {
					path = logins
				}
				path += "/" + url.PathEscape(args[0])
				if google && action == "remove" {
					path += "?domain=" + url.QueryEscape(domain)
				}
			case "list":
				path = logins + "?domain=" + url.QueryEscape(domain)
			case "get":
				path = logins + "/" + url.PathEscape(args[0])
			case "add", "set-password", "set-cert", "enable", "disable":
				method, path, body = "PATCH", logins, map[string]any{}
				if action == "add" {
					method = "POST"
					body["domain"], body["email"] = domain, email
					if inactive {
						body["is_active"] = false
					}
				} else {
					path += "/" + url.PathEscape(args[0])
					if google {
						body["domain"] = domain
					}
				}
				if action == "enable" || action == "disable" {
					body["is_active"] = action == "enable"
				}
				if action == "set-password" || action == "add" && !google {
					password, err := loginPassword(cmd, stdin)
					if err != nil {
						return nil, err
					}
					body["password"] = password
				}
				if action == "set-cert" || action == "add" && google {
					for flag, file := range map[string]string{"sso_private_key_pem": key, "sso_cert_pem": cert} {
						b, err := os.ReadFile(file)
						if err != nil {
							return nil, usageErr("could not read PEM file: "+file, "")
						}
						body[flag] = string(b)
					}
				}
			}
			var payload any
			if body != nil {
				payload = body
			}
			v, err := requestJSON(cmd, c, method, path, payload)
			return safeCredentialResult(v, err, body)

		})
		sub.Short = strings.ReplaceAll(action, "-", " ") + " (" + platform + ")"
		f := sub.Flags()
		required := func(flag string, value *string) { f.StringVar(value, flag, "", flag); _ = sub.MarkFlagRequired(flag) }
		if action == "list" || action == "add" || google && (action == "set-cert" || action == "enable" || action == "disable" || action == "remove") {
			required("domain", &domain)
		}
		if action == "add-domain" || action == "update-domain" {
			f.StringVar(&name, "name", "", "friendly name")
			if google {
				def := ""
				if action == "add-domain" {
					def = "always"
				}
				f.StringVar(&mode, "login-mode", def, "always | if_required")
			}
		}
		if action == "add" {
			required("email", &email)
			f.BoolVar(&inactive, "inactive", false, "register disabled")
		}
		if action == "set-password" || action == "add" && !google {
			f.BoolVar(&stdin, "password-stdin", false, "read password from stdin (never argv)")
		}
		if action == "set-cert" || action == "add" && google {
			required("key", &key)
			required("cert", &cert)
		}
		if action == "remove" || action == "remove-domain" {
			f.BoolVar(&yes, "yes", false, "skip confirmation")
		}
		cmd.AddCommand(sub)
	}
	return cmd
}

func redactLoginValue(v any, secret string) any {
	switch v := v.(type) {
	case string:
		return strings.ReplaceAll(v, secret, "[redacted]")
	case map[string]any:
		for k, value := range v {
			if k == "password" || k == "sso_private_key_pem" || k == "google_client_secret" || k == "google_refresh_token" {
				v[k] = "[redacted]"
				continue
			}
			v[k] = redactLoginValue(value, secret)
		}
	case []any:
		for i, value := range v {
			v[i] = redactLoginValue(value, secret)
		}
	}
	return v
}

// Defend against servers reflecting write-only credentials in successes or errors.
func safeCredentialResult(v any, err error, body map[string]any) (any, error) {
	var secrets []string
	for _, k := range []string{"password", "sso_private_key_pem", "sso_cert_pem", "google_client_secret", "google_refresh_token"} {
		if secret, ok := body[k].(string); ok && secret != "" {
			secrets = append(secrets, secret)
		}
	}
	v = redactLoginValue(v, "\x00")
	for _, secret := range secrets {
		v = redactLoginValue(v, secret)
	}
	if err != nil {
		// Redact the raw API body before Detail clips it, including long secrets.
		var ae *api.Error
		if errors.As(err, &ae) {
			copy := *ae
			var payload any
			if json.Unmarshal([]byte(copy.Body), &payload) == nil {
				payload = redactLoginValue(payload, "\x00")
				for _, secret := range secrets {
					payload = redactLoginValue(payload, secret)
				}
				encoded, _ := json.Marshal(payload)
				copy.Body = string(encoded)
			} else {
				for _, secret := range secrets {
					copy.Body = strings.ReplaceAll(copy.Body, secret, "[redacted]")
				}
			}
			err = &copy
		}
		mapped := apiError(err)
		message := mapped.Error()
		for _, secret := range secrets {
			message = strings.ReplaceAll(message, secret, "[redacted]")
			encoded, _ := json.Marshal(secret)
			message = strings.ReplaceAll(message, string(encoded[1:len(encoded)-1]), "[redacted]")
		}
		if ee, ok := mapped.(*ExitError); ok {
			copy := *ee
			copy.Message = message
			err = &copy
		} else {
			err = fmt.Errorf("%s", message)
		}
	}
	return v, err
}
