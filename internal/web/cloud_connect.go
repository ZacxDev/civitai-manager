package web

import (
	"strings"
)

// comfyCloudSettingKey is the `settings` row holding the WEB-set comfy_cloud
// toggle ("1" on, anything else off). It is ordinary UI state, exactly like
// match_remote / theme / the NSFW mode — NOT a credential.
//
// Cloud auth introduces NO new secret: the orchestration client is built from the
// already-configured CivitAI Token (Server.cloud → comfy.NewCloudClient(_,
// cfg.Token) → `Authorization: Bearer <token>`). Nothing secret is ever written
// to the settings table by this feature, which is why the DB file's mode is left
// alone.
const comfyCloudSettingKey = "comfy_cloud"

// cloudEnabledFromConfig reports the config FILE's answer for comfy_cloud and
// whether the file actually gave one. An explicit `comfy_cloud:` in the config
// file WINS over the DB toggle — see cloudEnabled.
func (s *Server) cloudEnabledFromConfig() (enabled, configured bool) {
	if s.cfg.ComfyCloud == nil {
		return false, false
	}
	return *s.cfg.ComfyCloud, true
}

// cloudEnabled resolves the EFFECTIVE comfy_cloud state.
//
// Precedence, deliberately explicit (two sources silently disagreeing is worse
// than either alone):
//
//  1. An explicit `comfy_cloud:` in the config file wins, true or false. The web
//     toggle then renders read-only and says where the value comes from.
//  2. Otherwise the DB `settings` row set by the web toggle governs.
//  3. Otherwise OFF. Cloud runs egress the graph to civitai.com and spend Buzz,
//     so the default — and the answer on any store error — is off (fail closed).
func (s *Server) cloudEnabled() bool {
	if enabled, configured := s.cloudEnabledFromConfig(); configured {
		return enabled
	}
	v, err := s.store.GetSettingDefault(comfyCloudSettingKey, "0")
	if err != nil {
		// Fail CLOSED: never turn on an egress + Buzz-spending feature because a
		// settings read failed.
		s.log.Warn("read comfy_cloud setting", "err", err)
		return false
	}
	return v == "1"
}

// cloudTokenConfigured reports whether ANY layer supplied a CivitAI token. The
// token itself is never returned from here.
func (s *Server) cloudTokenConfigured() bool {
	return strings.TrimSpace(s.cfg.Token) != ""
}
