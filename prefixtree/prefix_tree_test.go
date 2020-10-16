package prefixtree

import (
    "testing"
)

func TestAdd(t *testing.T) {
    testCases := []struct{
        name string
        toAdd []string
        expectedTree string
    }{
        {
            name: "empty tree is empty",
            toAdd: []string{},
        },
        {
            name: "word in tree",
            toAdd: []string{"word"},
        },
        {
            name: "word and woken in tree",
            toAdd: []string{"word", "woken"},
        },
        {
            name: "word and woken in tree",
            toAdd: []string{"word", "woken", "bird", "token", "tolkien", "abra", "abracadabdra", "acab", "even", "your", "uncle"},
        },

    }
    for _, tc := range testCases {
        tree := NewPrefixTree()
        for _, s := range tc.toAdd {
            tree.Add(s)
        }
        for _, s := range tc.toAdd {
            if !tree.Contains(s) {
                t.Errorf("%s failed: expected tree to contain the word %s.", tc.name, s)
            }
        }

    }
}
