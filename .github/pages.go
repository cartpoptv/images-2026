package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// up to 320 px wide = screenshot
const maxScreenshotWidth = 320

type Manifest struct {
	Version int     `json:"version"`
	Images  []Image `json:"images"`
}

type Image struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int64  `json:"bytes"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	name := filepath.Base(dir)
	if repo := os.Getenv("GITHUB_REPOSITORY"); repo != "" {
		name = path.Base(repo)
	}
	base := "https://cartpoptv.github.io/" + name + "/"

	var images []Image
	var problems []string
	folders, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		if !folder.IsDir() || strings.HasPrefix(folder.Name(), ".") {
			continue
		}
		files, err := os.ReadDir(folder.Name())
		if err != nil {
			return err
		}
		for _, file := range files {
			switch strings.ToLower(path.Ext(file.Name())) {
			case ".jpg", ".jpeg", ".png", ".webp", ".avif":
			default:
				continue
			}
			p := folder.Name() + "/" + file.Name()
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			w, h, err := size(data)
			if err != nil {
				problems = append(problems, p+": "+err.Error())
				continue
			}
			images = append(images, Image{p, w, h, int64(len(data))})
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Path < images[j].Path })

	manifest, err := json.MarshalIndent(Manifest{Version: 1, Images: images}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile("manifest.json", append(manifest, '\n'), 0o644); err != nil {
		return err
	}
	var page bytes.Buffer
	if err := indexPage.Execute(&page, listing(name, base, images)); err != nil {
		return err
	}
	fmt.Printf("%s: %d images\n", name, len(images))
	return os.WriteFile("index.html", page.Bytes(), 0o644)
}

type folderView struct {
	Name   string
	Images []imageView
	All    string
}

type imageView struct {
	Image
	Name, Kind, Markdown string
	KB                   int64
}

func listing(name, base string, images []Image) (view struct {
	Name    string
	Folders []folderView
}) {
	view.Name = name
	for _, img := range images {
		folder, file := path.Split(img.Path)
		folder = strings.TrimSuffix(folder, "/")
		if n := len(view.Folders); n == 0 || view.Folders[n-1].Name != folder {
			view.Folders = append(view.Folders, folderView{Name: folder})
		}
		f := &view.Folders[len(view.Folders)-1]
		v := imageView{Image: img, Name: file, KB: (img.Bytes + 1023) / 1024, Kind: "photo"}
		switch {
		case strings.HasPrefix(file, "og-image."):
			v.Kind = "og-image"
		case img.Width <= maxScreenshotWidth:
			v.Kind = "screenshot"
		}
		if v.Kind != "og-image" {
			v.Markdown = "![](" + base + img.Path + ")"
			f.All += v.Markdown + "\n"
		}
		f.Images = append(f.Images, v)
	}
	return view
}

var indexPage = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Name}}</title>
<style>
:root { color-scheme: light dark; font: 15px/1.4 system-ui, sans-serif; }
body { margin: 1.5rem auto; max-width: 72rem; padding: 0 1rem; }
summary { font-weight: 700; cursor: pointer; margin: 1rem 0; }
.item { display: grid; grid-template-columns: 200px 1fr; gap: 1rem; align-items: center; margin: 0.6rem 0; }
img { max-width: 200px; max-height: 130px; }
.screenshot img { image-rendering: pixelated; }
input, textarea { width: 100%; box-sizing: border-box; font: 13px ui-monospace, monospace; }
small { opacity: 0.7; }
</style>
</head>
<body>
<h1>{{.Name}}</h1>
{{range .Folders}}<details open><summary>{{.Name}}</summary>
{{range .Images}}<div class="item {{.Kind}}"><img src="{{.Path}}" alt="" loading="lazy"><div><b>{{.Name}}</b> <small>{{.Width}}x{{.Height}} · {{.KB}} KB · {{.Kind}}</small>{{if .Markdown}}<input readonly value="{{.Markdown}}" onclick="this.select()">{{end}}</div></div>
{{end}}<textarea readonly rows="8" onclick="this.select()">{{.All}}</textarea>
</details>
{{end}}</body>
</html>
`))

func size(b []byte) (w, h int, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("the size cannot be read: is the file complete?")
		}
	}()
	switch {
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return webpSize(b)
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return avifSize(b)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(b))
	return config.Width, config.Height, err
}

func webpSize(b []byte) (int, int, error) {
	p := b[20:]
	switch string(b[12:16]) {
	case "VP8 ": // lossy
		return int(binary.LittleEndian.Uint16(p[6:]) & 0x3fff), int(binary.LittleEndian.Uint16(p[8:]) & 0x3fff), nil
	case "VP8L": // lossless
		v := binary.LittleEndian.Uint32(p[1:])
		return int(v&0x3fff) + 1, int(v>>14&0x3fff) + 1, nil
	case "VP8X": // extended
		return (int(p[4]) | int(p[5])<<8 | int(p[6])<<16) + 1, (int(p[7]) | int(p[8])<<8 | int(p[9])<<16) + 1, nil
	}
	return 0, 0, errors.New("unknown kind of WebP")
}

func avifSize(b []byte) (int, int, error) {
	meta := child(boxes(b), "meta")
	inner := boxes(meta[4:])
	pitm := child(inner, "pitm")
	primary := uint32(binary.BigEndian.Uint16(pitm[4:]))
	if pitm[0] != 0 {
		primary = binary.BigEndian.Uint32(pitm[4:])
	}
	iprp := boxes(child(inner, "iprp"))
	props, ipma := boxes(child(iprp, "ipco")), child(iprp, "ipma")

	version, flags, p := ipma[0], ipma[3], ipma[8:]
	var w, h int
	turned := false
	for range binary.BigEndian.Uint32(ipma[4:]) {
		var id uint32
		if version < 1 {
			id, p = uint32(binary.BigEndian.Uint16(p)), p[2:]
		} else {
			id, p = binary.BigEndian.Uint32(p), p[4:]
		}
		n := int(p[0])
		p = p[1:]
		for range n {
			var i int
			if flags&1 != 0 {
				i, p = int(binary.BigEndian.Uint16(p)&0x7fff), p[2:]
			} else {
				i, p = int(p[0]&0x7f), p[1:]
			}
			if id != primary || i < 1 || i > len(props) {
				continue
			}
			switch prop := props[i-1]; prop.typ {
			case "ispe":
				w, h = int(binary.BigEndian.Uint32(prop.data[4:])), int(binary.BigEndian.Uint32(prop.data[8:]))
			case "irot":
				turned = prop.data[0]&1 == 1
			}
		}
	}
	if w == 0 {
		return 0, 0, errors.New("no size in the AVIF file")
	}
	if turned {
		w, h = h, w
	}
	return w, h, nil
}

type box struct {
	typ  string
	data []byte
}

func boxes(b []byte) []box {
	var out []box
	for len(b) >= 8 {
		n, head := int(binary.BigEndian.Uint32(b)), 8
		switch n {
		case 0:
			n = len(b)
		case 1:
			n, head = int(binary.BigEndian.Uint64(b[8:])), 16
		}
		out = append(out, box{string(b[4:8]), b[head:n]})
		b = b[n:]
	}
	return out
}

func child(bs []box, typ string) []byte {
	for _, b := range bs {
		if b.typ == typ {
			return b.data
		}
	}
	panic("no " + typ + " box")
}
