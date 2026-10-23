package memory

import (
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// maxProjectDirName is the longest projects/<name> folder name Claude Code
// writes in full; a longer encoded name is cut to this length and suffixed
// with a hash of the full path.
const maxProjectDirName = 200

// EncodeProjectDir mirrors Claude Code's encoding of a working directory into a
// projects/<name> folder: every UTF-16 code unit other than an ASCII letter or
// digit becomes a dash, so a character outside the BMP (an emoji) becomes two. This is used ONLY to locate the auto-memory directory; the
// working directory itself always comes from the transcript's cwd, never from
// decoding a folder name.
//
// Verified against real folders, e.g.
//
//	/home/dansarpong/ARMS/arms-infrastructure-as-code
//	  -> -home-dansarpong-ARMS-arms-infrastructure-as-code
//	/home/u/Personal/others/boafo/.claude/worktrees/bold-noyce-8797c8
//	  -> -home-u-Personal-others-boafo--claude-worktrees-bold-noyce-8797c8
//
// The result is never shortened: a name over maxProjectDirName characters is
// matched by prefix when locating auto memory (see projectMemoryDir).
func EncodeProjectDir(dir string) string {
	var b strings.Builder
	for _, r := range filepath.Clean(dir) {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if utf16.RuneLen(r) == 2 {
			b.WriteString("--")
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
