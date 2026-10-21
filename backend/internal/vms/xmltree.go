package vms

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// node is a minimal mutable XML tree used to edit libvirt domain XML.
// encoding/xml cannot round-trip arbitrary documents (unknown elements are
// lost, namespaces are rewritten), so documents are read with RawToken (prefixes
// kept as written) and written back verbatim apart from the edits.
type node struct {
	name  string // with its prefix, e.g. "libosinfo:os"
	attrs []xml.Attr
	kids  []any // *node | xml.CharData | xml.Comment
}

func parseXML(s string) (*node, error) {
	d := xml.NewDecoder(strings.NewReader(s))
	var stack []*node
	var root *node
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: rawName(t.Name), attrs: append([]xml.Attr(nil), t.Attr...)}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("unbalanced XML")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, xml.CharData(append([]byte(nil), t...)))
			}
		case xml.Comment:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, xml.Comment(append([]byte(nil), t...)))
			}
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, fmt.Errorf("invalid XML document")
	}
	return root, nil
}

func rawName(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func (n *node) String() string {
	var b bytes.Buffer
	n.write(&b)
	return b.String()
}

func (n *node) write(b *bytes.Buffer) {
	b.WriteString("<" + n.name)
	for _, a := range n.attrs {
		b.WriteString(" " + rawName(a.Name) + `="`)
		_ = xml.EscapeText(b, []byte(a.Value))
		b.WriteString(`"`)
	}
	if len(n.kids) == 0 {
		b.WriteString("/>")
		return
	}
	b.WriteString(">")
	for _, k := range n.kids {
		switch k := k.(type) {
		case *node:
			k.write(b)
		case xml.CharData:
			_ = xml.EscapeText(b, k)
		case xml.Comment:
			b.WriteString("<!--" + string(k) + "-->")
		}
	}
	b.WriteString("</" + n.name + ">")
}

// children returns the direct child elements called name ("" = all).
func (n *node) children(name string) []*node {
	var out []*node
	for _, k := range n.kids {
		if c, ok := k.(*node); ok && (name == "" || c.name == name) {
			out = append(out, c)
		}
	}
	return out
}

// child returns the first direct child called name, or nil.
func (n *node) child(name string) *node {
	if c := n.children(name); len(c) > 0 {
		return c[0]
	}
	return nil
}

// path walks "a/b/c" from n and returns every matching element.
func (n *node) path(p string) []*node {
	cur := []*node{n}
	for _, part := range strings.Split(p, "/") {
		var next []*node
		for _, c := range cur {
			next = append(next, c.children(part)...)
		}
		cur = next
	}
	return cur
}

// ensure returns the child called name, creating it when missing.
func (n *node) ensure(name string) *node {
	if c := n.child(name); c != nil {
		return c
	}
	c := &node{name: name}
	n.kids = append(n.kids, c)
	return c
}

func (n *node) attr(k string) string {
	for _, a := range n.attrs {
		if rawName(a.Name) == k {
			return a.Value
		}
	}
	return ""
}

func (n *node) setAttr(k, v string) {
	for i, a := range n.attrs {
		if rawName(a.Name) == k {
			n.attrs[i].Value = v
			return
		}
	}
	sp, local, ok := strings.Cut(k, ":")
	if !ok {
		sp, local = "", k
	}
	n.attrs = append(n.attrs, xml.Attr{Name: xml.Name{Space: sp, Local: local}, Value: v})
}

func (n *node) delAttr(k string) {
	out := n.attrs[:0]
	for _, a := range n.attrs {
		if rawName(a.Name) != k {
			out = append(out, a)
		}
	}
	n.attrs = out
}

// text returns the concatenated character data of n.
func (n *node) text() string {
	var b strings.Builder
	for _, k := range n.kids {
		if c, ok := k.(xml.CharData); ok {
			b.Write(c)
		}
	}
	return strings.TrimSpace(b.String())
}

func (n *node) setText(s string) {
	n.kids = []any{xml.CharData(s)}
}

// remove deletes the direct children for which drop returns true.
func (n *node) remove(drop func(c *node) bool) {
	out := n.kids[:0]
	for _, k := range n.kids {
		if c, ok := k.(*node); ok && drop(c) {
			continue
		}
		out = append(out, k)
	}
	n.kids = out
}

// walk calls fn on n and every element below it.
func (n *node) walk(fn func(*node)) {
	fn(n)
	for _, c := range n.children("") {
		c.walk(fn)
	}
}

// el builds an element from a small XML fragment (trusted, built by us).
func el(fragment string) *node {
	n, err := parseXML(fragment)
	if err != nil {
		panic(err)
	}
	return n
}
