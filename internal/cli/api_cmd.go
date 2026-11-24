package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var apiMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// newAPICmd is `meetstream api`: one MeetStream API call with the CLI's own
// sign-in. Apps made by `init` use it on a laptop, so no key is copied into
// project files. It talks only to the configured API URL.
func newAPICmd() *cobra.Command {
	var method, data string
	var envelope bool
	cmd := &cobra.Command{
		Use: "api <path>", Short: "Call the MeetStream API with your sign-in (for apps and scripts; no key needed)", GroupID: "tool",
		Args: cobra.ExactArgs(1),
		Long: `Make one MeetStream API call with the key the CLI stores after "meetstream login".

The path is relative to the API URL, for example /bots/<id>/detail. The answer
body is printed on stdout. A non-2xx answer exits 1. With --envelope it prints
{"status": <code>, "body": <answer>} and exits 0 for any HTTP answer, which is
what the init templates use.

  meetstream api /bots
  meetstream api /bots/create_bot -X POST -d '{"meeting_link": "..."}'
  echo '{...}' | meetstream api /bots/create_bot -X POST -d -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "://") || strings.ContainsAny(path, " \r\n") {
				return usageErr("path must be relative to the API, like /bots/<id>/detail", "")
			}
			method = strings.ToUpper(method)
			if !apiMethods[method] {
				return usageErr("unknown method "+method, "use GET, POST, PUT, PATCH or DELETE")
			}
			var body io.Reader
			if data != "" {
				raw := []byte(data)
				if data == "-" {
					b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4<<20))
					if err != nil {
						return err
					}
					raw = b
				}
				if !json.Valid(raw) {
					return usageErr("-d must be JSON", "")
				}
				body = bytes.NewReader(raw)
			}
			c, err := apiClient(cmd)
			if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(cmd.Context(), method, c.BaseURL+path, body)
			if err != nil {
				return usageErr("bad path: "+err.Error(), "")
			}
			req.Header.Set("Authorization", "Token "+c.Key)
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", c.UserAgent)
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			res, err := c.HTTP.Do(req)
			if err != nil {
				return &ExitError{Code: 1, Message: "could not reach the MeetStream API", Hint: "check your network, or MEETSTREAM_API_URL"}
			}
			defer res.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
			if err != nil {
				return err
			}
			var parsed any
			if len(raw) == 0 {
				parsed = nil
			} else if json.Unmarshal(raw, &parsed) != nil {
				parsed = string(raw)
			}
			if envelope {
				b, _ := json.Marshal(map[string]any{"status": res.StatusCode, "body": parsed})
				fmt.Fprintln(os.Stdout, string(b))
				return nil
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(parsed)
			if res.StatusCode/100 != 2 {
				code := 1
				if res.StatusCode == 401 || res.StatusCode == 403 {
					code = 2
				}
				return &ExitError{Code: code, Message: fmt.Sprintf("the API answered %d", res.StatusCode), Silent: g.JSON}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&method, "method", "X", "GET", "HTTP method")
	f.StringVarP(&data, "data", "d", "", "JSON body, or - to read it from stdin")
	f.BoolVar(&envelope, "envelope", false, `print {"status", "body"} and exit 0 for any HTTP answer`)
	return cmd
}
