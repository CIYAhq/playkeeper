package panel

// A server's own AI keys (from 0.4.9), which the AI Build Battle plugin
// builds with. The agent of the machine that runs the server keeps them
// beside the server's world and only ever says whether one is set; the
// panel forwards a key as it forwards any body and keeps nothing of it.

func init() {
	pathKeys = append(pathKeys, "provider")
}

// aiKeyRoutes take the Files tab's rights: whoever may change a server's
// files can add a plugin that reads its key, and a key spends its owner's
// money.
func (s *Server) aiKeyRoutes() []Route {
	return []Route{
		{"GET", "/api/servers/{id}/ai-keys", needSession, actViewFiles, s.serverProxy("GET", "/v1/servers/{id}/ai-keys")},
		{"PUT", "/api/servers/{id}/ai-keys/{provider}", needSessionCSRF, actEditFiles, s.serverProxy("PUT", "/v1/servers/{id}/ai-keys/{provider}")},
		{"DELETE", "/api/servers/{id}/ai-keys/{provider}", needSessionCSRF, actEditFiles, s.serverProxy("DELETE", "/v1/servers/{id}/ai-keys/{provider}")},
	}
}
