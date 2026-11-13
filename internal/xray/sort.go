package xray

import "sort"

func sortStrings(s []string) { sort.Strings(s) }

func sortClients(c []map[string]any) {
	sort.Slice(c, func(i, j int) bool {
		ei, _ := c[i]["email"].(string)
		ej, _ := c[j]["email"].(string)
		return ei < ej
	})
}
