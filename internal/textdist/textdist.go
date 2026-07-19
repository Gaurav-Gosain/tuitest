// Package textdist provides the string distance behind "did you mean" hints.
// It lives in its own package because both the command registry and the tape
// parser suggest corrections, and they cannot import each other.
package textdist

// Distance is the Damerau-Levenshtein distance (optimal string alignment)
// between a and b. It counts a transposition as one edit rather than two,
// because swapped letters are the most common typo of all: "rnu" should suggest
// "run" rather than being written off as too far away.
func Distance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n, m := len(ar), len(br)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, min(d[i][j-1]+1, d[i-1][j-1]+cost))
			if i > 1 && j > 1 && ar[i-1] == br[j-2] && ar[i-2] == br[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[n][m]
}

// Closest returns the candidate nearest to name, and whether it is close enough
// to be worth showing. The budget is roughly one edit per three characters and
// always at least one, so a single typo is forgiven without guessing wildly at
// an unrelated word.
func Closest(name string, candidates []string) (string, bool) {
	best, bestDist := "", -1
	for _, c := range candidates {
		if d := Distance(name, c); bestDist < 0 || d < bestDist {
			best, bestDist = c, d
		}
	}
	budget := max(len(name)/3, 1)
	if bestDist >= 0 && bestDist <= budget {
		return best, true
	}
	return "", false
}
