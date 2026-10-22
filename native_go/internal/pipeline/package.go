package pipeline

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/atlas"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/packageio"
)

func manifestGUID(name string) string {
	// RFC 4122 UUIDv5 with the URL namespace, matching uuid.uuid5 in builder.py.
	namespace, _ := hex.DecodeString("6ba7b8119dad11d180b400c04fd430c8")
	h := sha1.New()
	h.Write(namespace)
	h.Write([]byte("hd2-font-generator:" + name))
	b := h.Sum(nil)[:16]
	b[6] = (b[6] & 15) | 0x50
	b[8] = (b[8] & 63) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func hashFile(path string) (FileReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileReport{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return FileReport{}, err
	}
	return FileReport{n, hex.EncodeToString(h.Sum(nil))}, nil
}

func (s *buildState) verifyPatch(name string, paths [3]string, resources []archiveio.Resource) error {
	var files [3]*os.File
	var sizes [3]uint64
	defer func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
	}()
	for i, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		files[i] = f
		info, err := f.Stat()
		if err != nil {
			return err
		}
		sizes[i] = uint64(info.Size())
	}
	r, err := archiveio.NewReader(files[0], sizes[0])
	if err != nil {
		return err
	}
	index, err := archiveio.ReadIndex(r)
	if err != nil {
		return err
	}
	if err = index.ValidateBounds(sizes); err != nil {
		return err
	}
	if len(index.Entries) != len(resources) {
		return fmt.Errorf("saved patch resource count differs")
	}
	expected := make(map[[2]uint64]archiveio.Resource, len(resources))
	for _, r := range resources {
		expected[[2]uint64{r.Entry.NameID, r.Entry.TypeID}] = r
	}
	for _, entry := range index.Entries {
		if err = s.ctx.Err(); err != nil {
			return err
		}
		identity := [2]uint64{entry.NameID, entry.TypeID}
		want, ok := expected[identity]
		if !ok {
			return fmt.Errorf("unexpected saved resource %016x", entry.NameID)
		}
		delete(expected, identity)
		offsets := [3]uint64{entry.MainOffset, entry.StreamOffset, entry.GPUOffset}
		lengths := [3]uint32{entry.MainSize, entry.StreamSize, entry.GPUSize}
		payloads := [3][]byte{want.Main, want.Stream, want.GPU}
		for i, payload := range payloads {
			if uint64(lengths[i]) != uint64(len(payload)) {
				return fmt.Errorf("saved payload size differs")
			}
			h := sha256.New()
			if _, err = io.Copy(h, io.NewSectionReader(files[i], int64(offsets[i]), int64(lengths[i]))); err != nil {
				return err
			}
			if hex.EncodeToString(h.Sum(nil)) != inventory.Digest(payload) {
				return fmt.Errorf("saved resource %016x channel %d differs", entry.NameID, i)
			}
		}
		original := s.changed[fmt.Sprintf("%s/%016x/%016x", name, entry.TypeID, entry.NameID)]
		if original == nil {
			return fmt.Errorf("saved resource is outside replacement plan")
		}
		if original.Kind == "ttf" || original.Kind == "otf" || original.Kind == "runtime_font" || original.Kind == "font" {
			data, err := r.Read(entry.MainOffset, uint64(entry.MainSize))
			if err != nil {
				return err
			}
			if original.Kind == "font" {
				if _, err = atlas.GlyphKeys(data); err != nil {
					return err
				}
			} else if err = s.verifyFontPayload(original, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *buildState) packageOutput() error {
	started := time.Now()
	patchDir := filepath.Join(s.work, "patches")
	if err := os.Mkdir(patchDir, 0700); err != nil {
		return err
	}
	var archives []string
	for name := range s.changes {
		archives = append(archives, name)
	}
	sort.Strings(archives)
	var files []packageio.File
	s.progress("Writing and reading back font resources")
	// Archives are written and verified independently; results are recorded in
	// archive order afterwards.
	partPaths := func(name string) [3]string {
		return [3]string{filepath.Join(patchDir, name), filepath.Join(patchDir, name+".stream"), filepath.Join(patchDir, name+".gpu_resources")}
	}
	var patchJobs []func(context.Context) ([3]FileReport, error)
	for _, name := range archives {
		patchJobs = append(patchJobs, func(context.Context) (infos [3]FileReport, err error) {
			paths := partPaths(name)
			if _, err = archiveio.WritePatchParts(s.changes[name], paths); err != nil {
				return infos, err
			}
			if err = s.verifyPatch(name, paths, s.changes[name]); err != nil {
				return infos, err
			}
			for i, path := range paths {
				if infos[i], err = hashFile(path); err != nil {
					return infos, err
				}
			}
			return infos, nil
		})
	}
	written, err := runJobs(s.ctx, patchJobs, max(1, runtime.NumCPU()-2), nil)
	if err != nil {
		return err
	}
	for a, name := range archives {
		paths := partPaths(name)
		for i, suffix := range []string{"", ".stream", ".gpu_resources"} {
			member := "Fonts/" + name + ".patch_0" + suffix
			s.report.Files[member] = written[a][i]
			files = append(files, packageio.File{Name: member, Path: paths[i]})
		}
	}
	manifest := struct {
		Version     int
		Guid        string
		Name        string
		Description string
		Options     []struct {
			Name        string
			Description string
			Include     []string
		}
	}{Version: 1, Guid: manifestGUID(s.report.Name), Name: s.report.Name, Description: "Custom language fonts. Includes original glyph fallbacks. Generated locally."}
	manifest.Options = append(manifest.Options, struct {
		Name        string
		Description string
		Include     []string
	}{"自定义字体 / Custom fonts", "应用所选语言字体", []string{"Fonts"}})
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	files = append(files, packageio.File{Name: "manifest.json", Data: data})
	s.report.Files["manifest.json"] = FileReport{int64(len(data)), inventory.Digest(data)}
	s.report.OfflineReadbackVerified = true
	s.report.Performance["patch_readback_seconds"] = time.Since(started).Seconds()
	data, err = json.MarshalIndent(s.report, "", "  ")
	if err != nil {
		return err
	}
	files = append(files, packageio.File{Name: "font-build-report.json", Data: data}, packageio.File{Name: "README.txt", Data: []byte(readme)}, packageio.File{Name: "使用说明.txt", Data: []byte("\xef\xbb\xbf" + readmeChinese)})
	if err = os.MkdirAll(filepath.Dir(s.options.OutputZIP), 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(s.options.OutputZIP), ".hd2-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	pending := filepath.Join(stage, "verified.zip")
	s.progress("Packaging ZIP and verifying every saved member")
	packaged, err := packageio.WriteZIP(s.ctx, files, pending, packageio.Options{Level: s.options.ZIPLevel, Workers: s.options.Workers, WorkDir: s.work, Progress: func(p packageio.Progress) { s.progress(fmt.Sprintf("Packaging %d/%d: %s", p.Done, p.Total, p.Name)) }})
	if err != nil {
		return err
	}
	if err = s.ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(pending, s.options.OutputZIP); err != nil {
		return err
	}
	s.report.Output = s.options.OutputZIP
	s.report.ZIPSHA256 = packaged.SHA256
	s.report.ZIPBytes = packaged.Size
	s.report.Performance["packaging_seconds"] = time.Since(started).Seconds()
	return nil
}

const readme = `HD2 Custom Font Package

Close the game. Import this entire ZIP into Arsenal or a compatible HD2 mod manager.
Enable Custom fonts, deploy with the manager, then start the game.
To restore original fonts, disable this package and redeploy with the manager.
Avoid enabling other mods that replace the same font resources.

Only selected game font groups are replaced. Western languages share resources.
Missing characters use original fallback glyphs. This does not add game translations.
Font resources and packaging were checked offline; in-game rendering is unverified.
Check subtitles, player names, mixed scripts, colored text, scaling and outlines.
Do not redistribute selected fonts or fallback glyphs without the necessary permissions.
`

const readmeChinese = `HD2 自选字体 Mod

将整个 ZIP 导入 Arsenal 或兼容的 HD2 Mod Manager，启用「自定义字体」。
游戏完全退出后部署，再启动游戏。还原时在管理器停用此字体 Mod 并重新部署。
避免同时启用多个覆盖相同语言的字体 Mod。

西文语言共用字体；备用字库不会新增界面翻译。
当前包经过离线结构检查，游戏内的字号、换行和字幕仍需实测。
`
