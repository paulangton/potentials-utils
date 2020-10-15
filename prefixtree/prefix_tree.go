package prefixtree

import (
    "sort"
    "strings"
    "log"
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
        log.Printf(string(c))
        n, ok := next.children[c]
        if !ok {
            next.children[c] = newPrefixNode(c)
        }
        next = n
    }
}


// Contains returns true if there is a traversal from the root of the tree to a
// node in the tree whose prefixes form the given string, false otherwise
func (p *PrefixTree) Contains(s string) bool {
    next := p.Root
    contained := true
    for _, c := range s {
        n, ok := next.children[c]
        contained = contained && ok
        next = n
    }
    return contained
}

// String prints a BFS of the prefix tree
func (p *PrefixTree) String() string {
    next := p.Root
    q := next.childNodes()
    str := strings.Builder{}
    for len(q) > 0 {
        childPrefixes := next.childPrefixes()
        sort.Slice(childPrefixes, func(i,j int) bool {
            return childPrefixes[i] < childPrefixes[j]
        })
        for _, p := range childPrefixes {
            str.WriteRune(p)
            str.WriteRune(',')
        }
        // queue pop
        next, q := q[len(q)-1], q[:len(q)-1]
        q = append(q, next.childNodes()...)
    }
    return str.String()
}

