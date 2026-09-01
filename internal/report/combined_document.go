package report

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"strings"

	"k6-as-a-library/internal/artifact"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	combinedReportHeaderEnd = `</header>`
	combinedReportFooter    = `<footer>`
	combinedGraphsID        = "combined-graphs"
	combinedGraphsFrameID   = "combined-graphs-frame"
	combinedReportStyle     = `<style id="combined-report-style">
#combined-graphs { padding: 1.5rem; border-bottom: 1px solid #e2e8f0; }
#combined-graphs h2 { color: #2d3748; font-size: 1.5rem; margin: 0 0 1rem; }
#combined-graphs-frame { display: block; width: 100%; min-height: 48rem; border: 1px solid #e2e8f0; border-radius: 10px; background: #fff; }
#combined-tables { padding: 1.5rem; line-height: 1.4; overflow-x: auto; }
#combined-tables section { margin-top: 1.5rem; }
#combined-tables table { width: 100%; border-collapse: collapse; margin: 1rem 0 2rem; }
#combined-tables caption { color: #4a5568; font-weight: 600; margin: 0.5rem 0; text-align: left; }
#combined-tables th, #combined-tables td { border-bottom: 1px solid #e2e8f0; padding: 0.65rem 0.75rem; text-align: left; vertical-align: top; }
#combined-tables thead { background: #f7fafc; }
.combined-status { font-weight: 700; }
.combined-status-passed { color: #276749; }
.combined-status-failed { color: #c53030; }
.combined-status-not-evaluated, .combined-tag-missing, .combined-tag-null, .combined-tag-empty { color: #718096; }
.combined-tag-missing, .combined-tag-null, .combined-tag-empty { font-style: italic; }
.combined-diagnostic { border-left: 0.25rem solid #d69e2e; margin: 0.75rem 0; padding: 0.5rem 0.75rem; }
</style>`
)

func ComposeCombinedDocument(reporterDocument, dashboardDocument, tableFragment []byte) ([]byte, error) {
	if err := validateReporterDocument(reporterDocument); err != nil {
		return nil, err
	}
	reporterDocument, err := stripReporterExternalResources(reporterDocument)
	if err != nil {
		return nil, err
	}
	if err := validateDashboardDocument(dashboardDocument); err != nil {
		return nil, err
	}
	if err := validateCombinedTableFragment(tableFragment); err != nil {
		return nil, err
	}

	headEnd := bytes.Index(reporterDocument, []byte("</head>"))
	withStyle := make([]byte, 0, len(reporterDocument)+len(combinedReportStyle))
	withStyle = append(withStyle, reporterDocument[:headEnd]...)
	withStyle = append(withStyle, combinedReportStyle...)
	withStyle = append(withStyle, reporterDocument[headEnd:]...)

	headerEnd := bytes.Index(withStyle, []byte(combinedReportHeaderEnd)) + len(combinedReportHeaderEnd)
	graphs := combinedGraphsRegion(dashboardDocument)
	withGraphs := make([]byte, 0, len(withStyle)+len(graphs))
	withGraphs = append(withGraphs, withStyle[:headerEnd]...)
	withGraphs = append(withGraphs, graphs...)
	withGraphs = append(withGraphs, withStyle[headerEnd:]...)

	footer := bytes.Index(withGraphs, []byte(combinedReportFooter))
	combined := make([]byte, 0, len(withGraphs)+len(tableFragment))
	combined = append(combined, withGraphs[:footer]...)
	combined = append(combined, tableFragment...)
	combined = append(combined, withGraphs[footer:]...)

	if err := validateCombinedDocument(combined, dashboardDocument); err != nil {
		return nil, err
	}
	return combined, nil
}

func stripReporterExternalResources(document []byte) ([]byte, error) {
	root, err := xhtml.Parse(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("parse k6-reporter document: %w", err)
	}
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		for child := node.FirstChild; child != nil; {
			next := child.NextSibling
			href := attributeValue(child, "href")
			if child.Type == xhtml.ElementNode && child.Data == "link" && (strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "http://")) {
				node.RemoveChild(child)
			} else {
				visit(child)
			}
			child = next
		}
	}
	visit(root)
	var rendered bytes.Buffer
	if err := xhtml.Render(&rendered, root); err != nil {
		return nil, fmt.Errorf("serialize k6-reporter document: %w", err)
	}
	return rendered.Bytes(), nil
}

func validateReporterDocument(document []byte) error {
	if err := artifact.ValidateHTMLContents(document); err != nil {
		return fmt.Errorf("validate k6-reporter document: %w", err)
	}
	if count := bytes.Count(document, []byte("</head>")); count != 1 {
		return fmt.Errorf("k6-reporter document must contain exactly one closing head seam, found %d", count)
	}
	if count := bytes.Count(document, []byte(combinedReportHeaderEnd)); count != 1 {
		return fmt.Errorf("k6-reporter document must contain exactly one closing header seam, found %d", count)
	}
	if count := bytes.Count(document, []byte(combinedReportFooter)); count != 1 {
		return fmt.Errorf("k6-reporter document must contain exactly one footer seam, found %d", count)
	}
	if !bytes.Contains(document, []byte("K6 Reporter v")) {
		return errors.New("k6-reporter document marker is missing")
	}
	return nil
}

func validateCombinedTableFragment(fragment []byte) error {
	if len(bytes.TrimSpace(fragment)) == 0 {
		return errors.New("combined report table fragment is empty")
	}
	nodes, err := xhtml.ParseFragment(bytes.NewReader(fragment), &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"})
	if err != nil {
		return fmt.Errorf("parse combined report table fragment: %w", err)
	}
	var root *xhtml.Node
	for _, node := range nodes {
		if node.Type == xhtml.TextNode && strings.TrimSpace(node.Data) == "" {
			continue
		}
		if root != nil || node.Type != xhtml.ElementNode || node.Data != "section" {
			return errors.New("combined report table fragment must contain one section element")
		}
		root = node
	}
	if root == nil || attributeValue(root, "id") != "combined-tables" {
		return errors.New(`combined report table fragment section must have id "combined-tables"`)
	}
	var unsafe bool
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && (node.Data == "script" || node.Data == "iframe" || node.Data == "object" || node.Data == "embed") {
			unsafe = true
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	if unsafe {
		return errors.New("combined report table fragment contains executable content")
	}
	return nil
}

func validateDashboardDocument(document []byte) error {
	if err := artifact.ValidateHTMLContents(document); err != nil {
		return fmt.Errorf("validate dashboard document: %w", err)
	}
	if count := bytes.Count(document, []byte(`<div id="root"></div>`)); count != 1 {
		return fmt.Errorf("dashboard document must contain exactly one root marker, found %d", count)
	}
	if count := bytes.Count(document, []byte(DashboardDataTag)); count != 1 {
		return fmt.Errorf("dashboard document must contain exactly one data marker, found %d", count)
	}
	return nil
}

func combinedGraphsRegion(dashboardDocument []byte) []byte {
	escapedDashboard := html.EscapeString(string(dashboardDocument))
	return []byte(`<section id="combined-graphs" aria-labelledby="combined-graphs-heading"><h2 id="combined-graphs-heading">Interactive graphs</h2><iframe id="combined-graphs-frame" title="Interactive performance graphs" loading="eager" srcdoc="` + escapedDashboard + `"></iframe></section><script id="combined-graphs-resize">(() => { const frame = document.getElementById("combined-graphs-frame"); const resize = () => { const root = frame.contentDocument && frame.contentDocument.documentElement; if (root) frame.style.height = Math.max(root.scrollHeight, 768) + "px"; }; frame.addEventListener("load", () => { resize(); if (window.ResizeObserver) new ResizeObserver(resize).observe(frame.contentDocument.documentElement); }); })();</script>`)
}

func validateCombinedDocument(document, dashboardDocument []byte) error {
	if err := artifact.ValidateHTMLContents(document); err != nil {
		return fmt.Errorf("validate combined report: %w", err)
	}
	for _, id := range []string{combinedGraphsID, "combined-graphs-heading", combinedGraphsFrameID, "combined-tables", "combined-tables-heading", "combined-report-style", "combined-graphs-resize"} {
		count, err := countHTMLID(document, id)
		if err != nil {
			return fmt.Errorf("parse combined report: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("combined report must contain exactly one element with id %q, found %d", id, count)
		}
	}
	embedded, err := combinedDashboardDocument(document)
	if err != nil {
		return err
	}
	if !bytes.Equal(embedded, dashboardDocument) {
		return errors.New("combined report changed the embedded dashboard document")
	}
	return nil
}

func combinedDashboardDocument(document []byte) ([]byte, error) {
	root, err := xhtml.Parse(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("parse combined report: %w", err)
	}
	var dashboard []byte
	var visit func(*xhtml.Node) error
	visit = func(node *xhtml.Node) error {
		if node.Type == xhtml.ElementNode && node.Data == "iframe" && attributeValue(node, "id") == combinedGraphsFrameID {
			if dashboard != nil {
				return errors.New("combined report contains duplicate graph frames")
			}
			srcdoc := attributeValue(node, "srcdoc")
			if strings.TrimSpace(srcdoc) == "" {
				return errors.New("combined report graph frame has no dashboard document")
			}
			dashboard = []byte(srcdoc)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	if dashboard == nil {
		return nil, errors.New("combined report graph frame is missing")
	}
	return dashboard, nil
}

func CombinedDashboardDocument(document []byte) ([]byte, error) {
	return combinedDashboardDocument(document)
}

func combinedDashboardPayload(document []byte) ([]byte, error) {
	if bytes.Count(document, []byte(DashboardDataTag)) == 0 {
		embedded, err := combinedDashboardDocument(document)
		if err != nil {
			return nil, err
		}
		document = embedded
	}
	marker := []byte(DashboardDataTag)
	if bytes.Count(document, marker) != 1 {
		return nil, errors.New("dashboard report data marker must occur exactly once")
	}
	start := bytes.Index(document, marker) + len(marker)
	end := bytes.Index(document[start:], []byte("</script>"))
	if end < 0 {
		return nil, errors.New("dashboard report data marker has no closing script tag")
	}
	return bytes.Clone(document[start : start+end]), nil
}

func CombinedDashboardPayload(document []byte) ([]byte, error) {
	return combinedDashboardPayload(document)
}

func countHTMLID(document []byte, id string) (int, error) {
	root, err := xhtml.Parse(bytes.NewReader(document))
	if err != nil {
		return 0, err
	}
	count := 0
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && attributeValue(node, "id") == id {
			count++
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return count, nil
}

func attributeValue(node *xhtml.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}
	return ""
}
