package hub

// Markdown is parsed once on the server. The editor preview and both article
// entry points share this renderer; no browser library or CDN is needed.
import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var mdSpans = map[string]string{"lg": "md-lg", "sm": "md-sm", "serif": "md-serif", "mono": "md-mono"}
var reSpan = regexp.MustCompile(`^\{([a-z]+)\}(.*?)\{/([a-z]+)\}`)
var articleMarkdown, spanMarkdown goldmark.Markdown

func init() {
	// A span's contents are inline Markdown, including emphasis, links and code.
	// Giving this parser only paragraphs prevents block elements inside a span.
	spanMarkdown = goldmark.New(
		goldmark.WithParser(parser.NewParser(parser.WithBlockParsers(util.Prioritized(parser.NewParagraphParser(), 1000)), parser.WithInlineParsers(parser.DefaultInlineParsers()...))),
		goldmark.WithExtensions(extension.Strikethrough),
		goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(mdSafeRenderer{}, 100))),
	)
	articleMarkdown = goldmark.New(
		goldmark.WithExtensions(extension.GFM, highlighting.NewHighlighting(
			highlighting.WithGuessLanguage(false),
			highlighting.WithFormatOptions(chromahtml.WithClasses(true), chromahtml.PreventSurroundingPre(true), chromahtml.WithLineNumbers(false)),
			highlighting.WithCodeBlockOptions(func(highlighting.CodeBlockContext) []chromahtml.Option {
				return []chromahtml.Option{chromahtml.WithLineNumbers(false)}
			}),
			highlighting.WithWrapperRenderer(mdCodeWrapper),
		)),
		goldmark.WithParserOptions(parser.WithInlineParsers(util.Prioritized(mdSpanParser{}, 50))),
		goldmark.WithRendererOptions(gmhtml.WithHardWraps(), renderer.WithNodeRenderers(util.Prioritized(mdSafeRenderer{}, 100))),
	)
}

func RenderMarkdown(src string) template.HTML {
	var out bytes.Buffer
	if err := articleMarkdown.Convert([]byte(src), &out); err != nil {
		return template.HTML("<pre>" + html.EscapeString(src) + "</pre>")
	}
	return template.HTML(out.String())
}

func mdCodeWrapper(w util.BufWriter, ctx highlighting.CodeBlockContext, entering bool) {
	if !entering {
		_, _ = w.WriteString("</code></pre></div>\n")
		return
	}
	language, _ := ctx.Language()
	label := string(language)
	if label == "" {
		label = "text"
	}
	if len(label) > 32 {
		label = label[:32]
	}
	_, _ = w.WriteString(`<div class="md-codeblock"><div class="md-codehead"><span>` + html.EscapeString(label) + `</span><button type="button" data-copy-code>复制代码</button></div><pre class="chroma"><code class="language-` + html.EscapeString(label) + `">`)
}

type mdSpanNode struct {
	ast.BaseInline
	class, content string
}

var mdSpanKind = ast.NewNodeKind("KokoroStyleSpan")

func (n *mdSpanNode) Kind() ast.NodeKind            { return mdSpanKind }
func (n *mdSpanNode) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

type mdSpanParser struct{}

func (mdSpanParser) Trigger() []byte                     { return []byte{'{'} }
func (mdSpanParser) CloseBlock(ast.Node, parser.Context) {}
func (mdSpanParser) Parse(_ ast.Node, reader text.Reader, _ parser.Context) ast.Node {
	line, _ := reader.PeekLine()
	match := reSpan.FindSubmatch(line)
	if len(match) != 4 || !bytes.Equal(match[1], match[3]) {
		return nil
	}
	class, ok := mdSpans[string(match[1])]
	if !ok {
		return nil
	}
	reader.Advance(len(match[0]))
	return &mdSpanNode{class: class, content: string(match[2])}
}

// Raw HTML remains visible text. Only our fixed span classes, ordinary links
// and image destinations are allowed to create HTML from article content.
type mdSafeRenderer struct{}

func (mdSafeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindRawHTML, renderMDRawHTML)
	reg.Register(ast.KindHTMLBlock, renderMDHTMLBlock)
	reg.Register(ast.KindHeading, renderMDHeading)
	reg.Register(ast.KindLink, renderMDLink)
	reg.Register(ast.KindImage, renderMDImage)
	reg.Register(mdSpanKind, renderMDSpan)
}
func renderMDRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.RawHTML)
		for i := 0; i < n.Segments.Len(); i++ {
			seg := n.Segments.At(i)
			_, _ = w.WriteString(html.EscapeString(string(seg.Value(source))))
		}
	}
	return ast.WalkContinue, nil
}
func renderMDHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.HTMLBlock)
		for i := 0; i < n.Lines().Len(); i++ {
			seg := n.Lines().At(i)
			_, _ = w.WriteString(html.EscapeString(string(seg.Value(source))))
		}
		if n.HasClosure() {
			_, _ = w.WriteString(html.EscapeString(string(n.ClosureLine.Value(source))))
		}
	}
	return ast.WalkContinue, nil
}
func renderMDHeading(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	level := node.(*ast.Heading).Level + 2
	if level > 6 {
		level = 6
	}
	if entering {
		_, _ = fmt.Fprintf(w, "<h%d>", level)
	} else {
		_, _ = fmt.Fprintf(w, "</h%d>\n", level)
	}
	return ast.WalkContinue, nil
}
func mdDestination(raw []byte, image bool) (string, bool) {
	destination := html.UnescapeString(string(raw))
	if strings.TrimSpace(destination) != destination || strings.Contains(destination, `\`) || strings.ContainsFunc(destination, unicode.IsControl) || strings.HasPrefix(destination, "//") {
		return "", false
	}
	u, err := url.Parse(destination)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return "", false
		}
	case "mailto":
		if image {
			return "", false
		}
	case "":
	default:
		return "", false
	}
	return destination, true
}
func renderMDLink(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	destination, ok := mdDestination(n.Destination, false)
	if !ok {
		return ast.WalkContinue, nil
	}
	if entering {
		_, _ = w.WriteString(`<a href="` + html.EscapeString(destination) + `" rel="ugc nofollow noopener" target="_blank">`)
	} else {
		_, _ = w.WriteString("</a>")
	}
	return ast.WalkContinue, nil
}
func renderMDImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Image)
	alt := html.EscapeString(string(n.Text(source)))
	destination, ok := mdDestination(n.Destination, true)
	if !ok {
		_, _ = w.WriteString(alt)
		return ast.WalkSkipChildren, nil
	}
	_, _ = w.WriteString(`<img src="` + html.EscapeString(destination) + `" alt="` + alt + `" loading="lazy" decoding="async" referrerpolicy="no-referrer"`)
	if len(n.Title) > 0 {
		_, _ = w.WriteString(` title="` + html.EscapeString(string(n.Title)) + `"`)
	}
	_, _ = w.WriteString(">")
	return ast.WalkSkipChildren, nil
}
func renderMDSpan(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*mdSpanNode)
		var out bytes.Buffer
		if err := spanMarkdown.Convert([]byte(n.content), &out); err != nil {
			return ast.WalkStop, err
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(out.String()), "<p>"), "</p>")
		_, _ = w.WriteString(`<span class="` + n.class + `">` + inner + `</span>`)
	}
	return ast.WalkSkipChildren, nil
}
