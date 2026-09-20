package gitx

import "strings"

// RemoteBranches lists the remote-tracking branches nm already knows about,
// without touching the network. Completion uses it, so it answers instantly
// and reports nothing rather than failing.
func RemoteBranches(dir, remote string) []string {
	out, err := Run(dir, "for-each-ref", "--format=%(refname:lstrip=3)", "refs/remotes/"+remote)
	if err != nil {
		return nil
	}
	return ParseRefNames(out)
}

// ParseRefNames turns for-each-ref output into branch names, dropping the
// HEAD pointer that is not a branch anyone can check out.
func ParseRefNames(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || name == "HEAD" {
			continue
		}
		names = append(names, name)
	}
	return names
}
