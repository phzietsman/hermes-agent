package anthropic

import (
	"strings"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

// Image eviction keeps a request under the API's per-request image limit. Retiring an image edits
// a message the provider has already cached, and Anthropic matches its cache on an exact byte
// prefix, so a keep-newest-N window would miss the cache every turn. Instead images are held until
// the request would cross a real limit and then retired in batches.
const (
	// outboundImageLimit is where Anthropic applies a stricter per-image dimension cap (2000 px) to
	// every image in the request, counting images nested in tool_result content.
	outboundImageLimit = 20
	imageEvictionBatch = 8
	// outboundImageFloor shelters the newest frames only when uploads alone breach the ceiling.
	outboundImageFloor = 3
)

// imageRetireCount is how many of the OLDEST image-bearing tool results to retire. The count is a
// step function of the overshoot (it advances in quanta), because the pass is recomputed on every
// request: an exact count-minus-limit target would move the frontier with every new image.
// carrierBlocksNewestFirst is the image count per image-bearing tool result, newest first;
// reserved counts images the pass must never rewrite (user uploads).
func imageRetireCount(carrierBlocksNewestFirst []int, reserved int) int {
	total := len(carrierBlocksNewestFirst)
	kept := make([]int, total+1)
	for i, n := range carrierBlocksNewestFirst {
		kept[i+1] = kept[i] + n
	}
	fits := func(k int) bool { return reserved+kept[k] <= outboundImageLimit }
	if fits(total) {
		return 0
	}
	floor := min(max(outboundImageFloor, 0), total)
	maxRetire := total
	if !fits(0) {
		maxRetire = total - floor
	}
	if maxRetire <= 0 {
		return 0
	}
	window := 0
	if fits(0) {
		for k := total; k >= 0; k-- {
			if fits(k) {
				window = k
				break
			}
		}
	}
	quantum := max(1, min(imageEvictionBatch, window-floor))
	retire := 0
	for retire < maxRetire {
		retire = min(retire+quantum, maxRetire)
		if fits(total - retire) {
			break
		}
	}
	return retire
}

// anthropicImageTypes are the inline raster types the API accepts.
var anthropicImageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

// unsupportedInlineImageType is the image/<subtype> of a data:image/ URL the API rejects (SVG, BMP,
// TIFF and the like 400 every replay once in history; image/jpg counts as JPEG), or "" for accepted
// types and for URLs that are not data:image/ (the provider validates remote URLs).
func unsupportedInlineImageType(url string) string {
	header, _, _ := strings.Cut(url, ",")
	header = strings.ToLower(header)
	rest, ok := strings.CutPrefix(header, "data:image/")
	if !ok {
		return ""
	}
	sub, _, _ := strings.Cut(rest, ";")
	sub = strings.TrimSpace(sub)
	if sub == "" {
		sub = "unknown"
	}
	if sub == "jpg" {
		sub = "jpeg"
	}
	if t := "image/" + sub; !anthropicImageTypes[t] {
		return t
	}
	return ""
}

// imageBlockFromURL turns an OpenAI image URL or data URL into an Anthropic image block. An
// unsupported inline subtype becomes a text placeholder (SVG rasterising is not ported).
func imageBlockFromURL(url string) llm.Block {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "data:") {
		return llm.Block{"type": "image", "source": llm.Block{"type": "url", "url": url}}
	}
	if bad := unsupportedInlineImageType(url); bad != "" {
		return textBlock("[image omitted: " + bad + " is not a supported image format]")
	}
	header, data, _ := strings.Cut(url, ",")
	mime, _, _ := strings.Cut(header[len("data:"):], ";")
	mime = strings.TrimSpace(mime)
	if !strings.HasPrefix(mime, "image/") {
		mime = "image/jpeg"
	}
	if strings.EqualFold(mime, "image/jpg") {
		mime = "image/jpeg"
	}
	return llm.Block{"type": "image", "source": llm.Block{"type": "base64", "media_type": mime, "data": data}}
}
