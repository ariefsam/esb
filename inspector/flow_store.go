package inspector

func storeID(aggregate string) string { return "store:" + aggregate }

func union(a, b []string) []string {
	set := map[string]bool{}
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		set[s] = true
	}
	return sortedKeys(set)
}

// soleOf returns the only distinct value in list, or "" when there are several
// or none.
func soleOf(list []string) string {
	u := union(list, nil)
	if len(u) == 1 {
		return u[0]
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
