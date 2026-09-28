package gitx

import "strings"

// RefsContaining lists the refs from which a commit is reachable, excluding the
// named branch itself.
//
// This is how nm tells a leftover branch that holds work from one that holds none:
// if any other ref already contains the branch's tip, deleting the branch destroys
// nothing. `git branch -d` answers a narrower question — merged into its upstream or
// into HEAD — and refuses a branch cut from an unmerged predecessor even though that
// predecessor's branch still holds every commit on it.
func RefsContaining(dir, commit, excludeBranch string) []string {
	out, err := Run(dir, "for-each-ref", "--contains", commit, "--format=%(refname)")
	if err != nil {
		// No answer is the safe answer: the caller treats an empty list as "this
		// branch may hold unique work" and leaves it alone.
		return nil
	}
	skip := "refs/heads/" + excludeBranch
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || name == skip {
			continue
		}
		refs = append(refs, name)
	}
	return refs
}

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
