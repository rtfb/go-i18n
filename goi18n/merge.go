package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	//"launchpad.net/goyaml"
	"github.com/nicksnyder/go-i18n/i18n/bundle"
	"github.com/nicksnyder/go-i18n/i18n/locale"
	"github.com/nicksnyder/go-i18n/i18n/translation"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type mergeCommand struct {
	translationFiles []string
	sourceLocaleID   string
	outdir           string
	format           string
	sift             string
}

func (mc *mergeCommand) execute() error {
	if len(mc.translationFiles) < 1 {
		return fmt.Errorf("need at least one translation file to parse")
	}

	if _, err := locale.New(mc.sourceLocaleID); err != nil {
		return fmt.Errorf("invalid source locale %s: %s", mc.sourceLocaleID, err)
	}

	marshal, err := newMarshalFunc(mc.format)
	if err != nil {
		return err
	}

	bundle := bundle.New()
	for _, tf := range mc.translationFiles {
		if err := bundle.LoadTranslationFile(tf); err != nil {
			return fmt.Errorf("failed to load translation file %s because %s\n", tf, err)
		}
	}

	if mc.sift != "<unspecified>" {
		var v visitor
		allFiles := getAllFiles(mc.sift)
		fmt.Printf("%#v\n", allFiles)
		v.parseAllFiles(allFiles)
		if v.tFunc != "" {
			// Now when we have a tFunc, walk the files again, looking for the
			// strings:
			v.allStrings = make([]string, 0, 0)
			v.parseAllFiles(allFiles)
		} else {
			println("Warning: no Tfunc found!")
		}

		for _, str := range v.allStrings {
			xlat, err := translation.NewTranslation(map[string]interface{}{
				"id":          str[1:len(str)-1],
				"translation": "",
			})
			println(xlat.ID())
			if err != nil {
				fmt.Printf("Error adding string %q to a bundle\n", str)
			}
			bundle.AddTranslation(locale.MustNew(mc.sourceLocaleID), xlat)
		}
	}

	translations := bundle.Translations()
	sourceTranslations := translations[mc.sourceLocaleID]
	for translationID, src := range sourceTranslations {
		for _, localeTranslations := range translations {
			if dst := localeTranslations[translationID]; dst == nil || reflect.TypeOf(src) != reflect.TypeOf(dst) {
				localeTranslations[translationID] = src.UntranslatedCopy()
			}
		}
	}

	for localeID, localeTranslations := range translations {
		locale := locale.MustNew(localeID)
		all := filter(localeTranslations, func(t translation.Translation) translation.Translation {
			return t.Normalize(locale.Language)
		})
		if err := mc.writeFile("all", all, localeID, marshal); err != nil {
			return err
		}

		untranslated := filter(localeTranslations, func(t translation.Translation) translation.Translation {
			if t.Incomplete(locale.Language) {
				return t.Normalize(locale.Language).Backfill(sourceTranslations[t.ID()])
			}
			return nil
		})
		if err := mc.writeFile("untranslated", untranslated, localeID, marshal); err != nil {
			return err
		}
	}
	return nil
}

type visitor struct {
	allStrings []string
	tFunc      string
}

func getAllFiles(siftParam string) []string {
	var files []string
	if dir, err := isDir(siftParam); err == nil && dir {
		filepath.Walk(siftParam, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() && strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}
			return nil
		})
		return files
	}
	//if isGlob() {
		//return expandGlob()
	//}
	files = append(files, siftParam)
	return files
}

func isDir(file string) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	switch mode := fi.Mode(); {
	case mode.IsDir():
		return true, nil
	case mode.IsRegular():
		return false, nil
	}
	return false, nil
}

func (v *visitor) parseAllFiles(files []string) {
	for _, fileName := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, fileName, nil, 0)
		if err != nil {
			panic(err) // XXX: better error handling
		}
		ast.Walk(v, f)
	}
}

func getTFuncName(stmt *ast.AssignStmt) (string, bool) {
	name := ""
	if len(stmt.Lhs) > 0 {
		if id, ok := stmt.Lhs[0].(*ast.Ident); ok {
			name = id.Name
		}
	}
	for _, exp := range stmt.Rhs {
		if call, ok := exp.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				//fmt.Printf("sel.X=%+v\n", sel.X)
				//fmt.Printf("sel.Sel=%+v\n", *sel.Sel)
				if fmt.Sprintf("%s.%s", sel.X, (*sel.Sel).Name) == "i18n.MustTfunc" {
					return name, true
				}
			}
		}
	}
	return "", false
}

func (v *visitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.CallExpr:
		if v.tFunc == "" || v.allStrings == nil {
			return v // Don't do anything until we have tFunc
		}
		call, _ := n.Fun.(*ast.Ident)
		if call != nil && call.Name == v.tFunc {
			for _, a := range n.Args {
				switch b := a.(type) {
				case *ast.BasicLit:
					if b.Kind == token.STRING {
						fmt.Printf("%+v\n", b.Value)
						v.allStrings = append(v.allStrings, b.Value)
					}
				default:
					fmt.Printf("%#v\n", b)
				}
			}
		}
	case *ast.AssignStmt:
		if v.tFunc != "" {
			return v // Don't redefine tFunc if we already have one
		}
		if tFunc, ok := getTFuncName(n); ok {
			fmt.Printf("OK, tfunc = %q\n", tFunc)
			v.tFunc = tFunc
		}
	}
	return v
}

type marshalFunc func(interface{}) ([]byte, error)

func (mc *mergeCommand) writeFile(label string, translations []translation.Translation, localeID string, marshal marshalFunc) error {
	sort.Sort(translation.SortableByID(translations))
	buf, err := marshal(marshalInterface(translations))
	if err != nil {
		return fmt.Errorf("failed to marshal %s strings to %s because %s", localeID, mc.format, err)
	}
	filename := filepath.Join(mc.outdir, fmt.Sprintf("%s.%s.%s", localeID, label, mc.format))
	if err := ioutil.WriteFile(filename, buf, 0666); err != nil {
		return fmt.Errorf("failed to write %s because %s", filename, err)
	}
	return nil
}

func filter(translations map[string]translation.Translation, filter func(translation.Translation) translation.Translation) []translation.Translation {
	filtered := make([]translation.Translation, 0, len(translations))
	for _, translation := range translations {
		if t := filter(translation); t != nil {
			filtered = append(filtered, t)
		}
	}
	return filtered

}

func newMarshalFunc(format string) (marshalFunc, error) {
	switch format {
	case "json":
		return func(v interface{}) ([]byte, error) {
			return json.MarshalIndent(v, "", "  ")
		}, nil
		/*
			case "yaml":
				return func(v interface{}) ([]byte, error) {
					return goyaml.Marshal(v)
				}, nil
		*/
	}
	return nil, fmt.Errorf("unsupported format: %s\n", format)
}

func marshalInterface(translations []translation.Translation) []interface{} {
	mi := make([]interface{}, len(translations))
	for i, translation := range translations {
		mi[i] = translation.MarshalInterface()
	}
	return mi
}
