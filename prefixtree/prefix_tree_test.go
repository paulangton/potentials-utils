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
            expectedTree: "",
        },
        {
            name: "word in tree",
            toAdd: []string{"word"},
            expectedTree: "w,o,r,d,",
        },
    }
    for _, tc := range testCases {
        tree := NewPrefixTree()
        for _, s := range tc.toAdd {
            tree.Add(s)
        }
        actualTreeString := tree.String()
        if actualTreeString != tc.expectedTree {
            t.Errorf("%s failed: expected a tree string\n%s\nGot\n%s", tc.name, tc.expectedTree, actualTreeString)
        }

    }
}
