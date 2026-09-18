package poker

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestPureCoreImportsNothingButStdlib 是 ADR-0012 那条「纯核心」的看门人。
//
// 这条以前不需要写：整个模块一个第三方依赖都没有，想违反也无从违反。现在有了，
// 这条就得有人守——牌局核心一旦开始 import 外面的东西，`--seed` 可复现、
// 可见性测试、把一手牌单独重放出来这几件事就都建在别人的行为上了，
// 而那个「别人」下个版本改了什么不归我们管。
//
// 顺带守住依赖方向：这个包也不 import 任何其他内部包，反过来别人 import 它。
func TestPureCoreImportsNothingButStdlib(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for name, pkg := range pkgs {
		for file, f := range pkg.Files {
			// 测试文件不算：它们可以用任何东西，只要跑得起来。
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			for _, imp := range f.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				switch {
				case strings.HasPrefix(path, "github.com/xujnan/poker-cli/"):
					t.Errorf("%s（包 %s）import 了内部包 %q——依赖方向是单向朝内的（ADR-0012）",
						file, name, path)
				case strings.Contains(strings.SplitN(path, "/", 2)[0], "."):
					// 标准库的第一段里没有点；有点的是域名，也就是第三方。
					t.Errorf("%s（包 %s）import 了第三方库 %q——牌局核心只能用标准库（ADR-0012）",
						file, name, path)
				}
			}
		}
	}
}
