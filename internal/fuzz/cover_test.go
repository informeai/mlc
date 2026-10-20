package fuzz

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestCoverReport é o fim do make cover: lê o perfil de cobertura do
// checker (MLC_COVER) e lista os erros que nenhum teste dispara nos
// arquivos do verificador de posse. Cada um é uma regra que pode estar
// quebrada sem ninguém perceber: merece um teste em tests/errors (ou é
// código que nunca roda, porque outra regra pega antes).
func TestCoverReport(t *testing.T) {
	profile := os.Getenv("MLC_COVER")
	if profile == "" {
		t.Skip("rode com make cover")
	}
	f, err := os.Open(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	re := regexp.MustCompile(`^mlc/internal/checker/(\w+\.go):(\d+)\.\d+,(\d+)\.\d+ \d+ (\d+)$`)
	hit := map[string]map[int]bool{}  // arquivo → linha → executada
	seen := map[string]map[int]bool{} // linhas com código
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := re.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		file := m[1]
		a, _ := strconv.Atoi(m[2])
		b, _ := strconv.Atoi(m[3])
		n, _ := strconv.Atoi(m[4])
		if hit[file] == nil {
			hit[file], seen[file] = map[int]bool{}, map[int]bool{}
		}
		for l := a; l <= b; l++ {
			seen[file][l] = true
			if n > 0 {
				hit[file][l] = true
			}
		}
	}
	files := []string{"ownership.go", "borrows.go", "owned.go", "pointers.go", "slices.go", "enums.go", "structs.go", "threads.go", "chans.go", "mutex.go", "stack.go"}
	var b strings.Builder
	total := 0
	for _, file := range files {
		src, err := os.ReadFile(filepath.Join("..", "checker", file))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		var ls []int
		for l := range seen[file] {
			if !hit[file][l] && strings.Contains(lines[l-1], "errorf(") {
				ls = append(ls, l)
			}
		}
		sort.Ints(ls)
		for _, l := range ls {
			total++
			fmt.Fprintf(&b, "  %s:%d: %s\n", file, l, strings.TrimSpace(lines[l-1]))
		}
	}
	rep := fmt.Sprintf("\nerros do verificador que nenhum teste dispara: %d\n%s", total, b.String())
	t.Log(rep)
	os.WriteFile(filepath.Join(filepath.Dir(profile), "relatorio.txt"), []byte(rep), 0o644)
}
