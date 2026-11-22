package localapps

import "encoding/json"

const ShareTool = "share_local_app"
const StopTool = "stop_local_app"

func Tools() []map[string]any {
	var t []map[string]any
	_ = json.Unmarshal([]byte("[{\"name\": \"share_local_app\", \"description\": \"Make an app running on the owner's computer (localhost) viewable in the meeting. Opens a temporary public https address for the local port and returns it; then call screen_share with that url. screen_share alone cannot open localhost: that is the meeting bot's machine.\", \"inputSchema\": {\"type\": \"object\", \"properties\": {\"port\": {\"type\": \"integer\", \"minimum\": 1, \"maximum\": 65535, \"description\": \"local port\"}, \"path\": {\"type\": \"string\", \"description\": \"optional path, for example /dashboard\"}}, \"required\": [\"port\"]}}, {\"name\": \"stop_local_app\", \"description\": \"Close the address share_local_app opened (one port, or all when no port is given).\", \"inputSchema\": {\"type\": \"object\", \"properties\": {\"port\": {\"type\": \"integer\"}}}}]"), &t)
	return t
}
