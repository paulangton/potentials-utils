package prefixtree

import (
    "strings"
    "bytes"
)


// PrefixTree represents a prefix tree for a set of strings. The first level of
// the tree represents all characters that appear at index 0 in the set of
// strings, the second level all characters at index 1, and so on down the tree.
type PrefixTree struct {
    Root *prefixNode
}

// prefixNode is an element in a prefix tree which holds a prefix and a set of
// child prefixes representing runes that could follow the current rune.
type prefixNode struct {
    data rune
    children map[rune]*prefixNode
}

func (p *prefixNode) childPrefixes() []rune {
    keys := []rune{}
    for k, _ := range p.children {
        keys = append(keys, k)
    }
    return keys
}

func (p *prefixNode) childNodes() []*prefixNode {
    vals := []*prefixNode{}
    for _, v := range p.children {
        vals = append(vals, v)
    }
    return vals
}

func newPrefixNode(c rune) *prefixNode {
    return &prefixNode{
        data: c,
        children: map[rune]*prefixNode{},
    }
}

func NewPrefixTree() *PrefixTree {
    return &PrefixTree{
        Root: newPrefixNode('\\'),
    }
}


// Add adds the given string to the prefix tree. Every nth character in the
// provided string will occur in the nth level of the tree.
func (p *PrefixTree) Add(s string) {
    next := p.Root
    for _, c := range s {
        n, ok := next.children[c]
        if !ok {
            prefixNode := newPrefixNode(c)
            next.children[c] = prefixNode
            next = prefixNode
        } else {
            next = n
        }
    }
}


// Contains returns true if there is a traversal from the root of the tree to a
// node in the tree whose prefixes form the given string, false otherwise
func (p *PrefixTree) Contains(s string) bool {
    next := p.Root
    for _, c := range s {
        n, ok := next.children[c]
        if !ok {
            return false
        }
        next = n
    }
    return true
}

// String prints a BFS of the prefix tree. The only ordering guaranteed is that a rune at level
// n will be printed before a rune at level n+1
func (p *PrefixTree) String() string {
    next := p.Root
    q := next.childNodes()
    str := strings.Builder{}
    for len(q) > 0 {
        childPrefixes := next.childPrefixes()
        for _, p := range childPrefixes {
            str.WriteRune(p)
            str.WriteRune(',')
        }
        // queue pop
        next, q = q[len(q)-1], q[:len(q)-1]
        q = append(q, next.childNodes()...)
    }
    return str.String()
}


// Words prints a list of all words present in the prefix tree
func (p *PrefixTree) Words() []string {
    words := []string{}
    for _, n := range p.Root.childNodes() {
        words = append(words, p.wordsHelper(n, &bytes.Buffer{})...)
    }
    return words
}

func (p *PrefixTree) wordsHelper(n *prefixNode, word *bytes.Buffer) []string {
    // no error is returned from bytes.Buffer.WriteRune
    word.WriteRune(n.data)
    if len(n.children) == 0 {
        return []string{word.String()}
    } else {
        words := []string{}
        for _, c := range n.childNodes() {
            words = append(words, p.wordsHelper(c, bytes.NewBuffer(word.Bytes()))...)
        }
        return words
    }
}

// the docs say not to do this
func copyStringBuilder(b strings.Builder) strings.Builder {
    var newBuilder strings.Builder
    for _, c := range b.String() {
        newBuilder.WriteRune(c)
    }
    return b
}
