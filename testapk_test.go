package main

// Small but real apks for tests: a binary AndroidManifest.xml, an empty
// resources.arsc, a png icon and a v1 (JAR) signature, so that apkparser and
// apkverifier read them like apks from TAK.gov.

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/avast/apkverifier/fullsailor/pkcs7"
)

// testApk describes an apk to build.
type testApk struct {
	pkg         string
	label       string
	versionCode int
	versionName string
	pluginAPI   string // e.g. "com.atakmap.app@5.8.0.CIV"; empty for an app
	key         int    // signing key: 0 or 1
	iconColor   color.NRGBA
	vectorIcon  []vectorPath      // if set, the icon is a VectorDrawable res/icon.xml
	extra       map[string]string // more files, e.g. lib/armeabi-v7a/x.so
}

// vectorPath is a <path> of a test VectorDrawable (attributes as strings).
type vectorPath map[string]string

// writeTestApk writes the apk to path.
func writeTestApk(t *testing.T, path string, a testApk) {
	t.Helper()
	if a.versionName == "" {
		a.versionName = "1.0"
	}
	if a.iconColor == (color.NRGBA{}) {
		a.iconColor = color.NRGBA{0x20, 0x80, 0xff, 0xff}
	}
	files := map[string][]byte{
		"AndroidManifest.xml": testManifest(a),
		"resources.arsc":      emptyResourceTable(),
		"res/icon.png":        testPNG(a.iconColor),
		"res/icon.xml":        testVector(a.vectorIcon),
		"classes.dex":         []byte("dex\n035\x00" + a.pkg + fmt.Sprint(a.versionCode)),
	}
	for name, content := range a.extra {
		files[name] = []byte(content)
	}
	var names []string
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)

	// v1 signature: MANIFEST.MF with a digest of each file, CERT.SF with a
	// digest of each section of MANIFEST.MF, CERT.RSA signing CERT.SF
	var mf, sf bytes.Buffer
	mf.WriteString("Manifest-Version: 1.0\r\nCreated-By: taktool tests\r\n\r\n")
	var sections []string
	for _, n := range names {
		sum := sha1.Sum(files[n])
		section := fmt.Sprintf("Name: %s\r\nSHA1-Digest: %s\r\n\r\n", n, base64.StdEncoding.EncodeToString(sum[:]))
		mf.WriteString(section)
		sections = append(sections, n+"\x00"+section)
	}
	mfSum := sha1.Sum(mf.Bytes())
	fmt.Fprintf(&sf, "Signature-Version: 1.0\r\nCreated-By: taktool tests\r\nSHA1-Digest-Manifest: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(mfSum[:]))
	for _, s := range sections {
		n, section, _ := strings.Cut(s, "\x00")
		sum := sha1.Sum([]byte(section))
		fmt.Fprintf(&sf, "Name: %s\r\nSHA1-Digest: %s\r\n\r\n", n, base64.StdEncoding.EncodeToString(sum[:]))
	}
	cert, key := testSigningKey(t, a.key)
	sd, err := pkcs7.NewSignedData(sf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.AddSigner(cert, key, pkcs7.SignerInfoConfig{}); err != nil {
		t.Fatal(err)
	}
	sd.Detach()
	sig, err := sd.Finish()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	add("META-INF/MANIFEST.MF", mf.Bytes())
	add("META-INF/CERT.SF", sf.Bytes())
	add("META-INF/CERT.RSA", sig)
	for _, n := range names {
		add(n, files[n])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

var testKeys struct {
	sync.Mutex
	certs [2]*x509.Certificate
	keys  [2]*rsa.PrivateKey
}

func testSigningKey(t *testing.T, i int) (*x509.Certificate, *rsa.PrivateKey) {
	testKeys.Lock()
	defer testKeys.Unlock()
	if testKeys.certs[i] == nil {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(int64(i + 1)),
			Subject:      pkix.Name{CommonName: fmt.Sprintf("taktool test key %d", i)},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		testKeys.certs[i], testKeys.keys[i] = cert, key
	}
	return testKeys.certs[i], testKeys.keys[i]
}

func testPNG(c color.NRGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// emptyResourceTable is a resources.arsc with an empty string pool and no
// packages.
func emptyResourceTable() []byte {
	pool := stringPool(nil)
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint16(0x0002))
	le(uint16(12))
	le(uint32(12 + len(pool)))
	le(uint32(0)) // packages
	b.Write(pool)
	return b.Bytes()
}

// stringPool encodes a UTF-16 string pool chunk.
func stringPool(strs []string) []byte {
	var offsets, data bytes.Buffer
	for _, s := range strs {
		binary.Write(&offsets, binary.LittleEndian, uint32(data.Len()))
		u := utf16.Encode([]rune(s))
		binary.Write(&data, binary.LittleEndian, uint16(len(u)))
		binary.Write(&data, binary.LittleEndian, u)
		binary.Write(&data, binary.LittleEndian, uint16(0))
	}
	for data.Len()%4 != 0 {
		data.WriteByte(0)
	}
	const header = 28
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint16(0x0001))
	le(uint16(header))
	le(uint32(header + offsets.Len() + data.Len()))
	le(uint32(len(strs)))
	le(uint32(0)) // styles
	le(uint32(0)) // flags: UTF-16
	le(uint32(header + offsets.Len()))
	le(uint32(0))
	b.Write(offsets.Bytes())
	b.Write(data.Bytes())
	return b.Bytes()
}

// axml builds a binary XML document.
type axml struct {
	strs  []string
	index map[string]uint32
	body  bytes.Buffer
}

const androidNS = "http://schemas.android.com/apk/res/android"

func (x *axml) str(s string) uint32 {
	if x.index == nil {
		x.index = map[string]uint32{}
	}
	if i, ok := x.index[s]; ok {
		return i
	}
	x.index[s] = uint32(len(x.strs))
	x.strs = append(x.strs, s)
	return x.index[s]
}

type axmlAttr struct {
	name  string
	str   string
	num   int
	isNum bool
}

func sAttr(name, v string) axmlAttr { return axmlAttr{name: name, str: v} }
func nAttr(name string, v int) axmlAttr {
	return axmlAttr{name: name, num: v, isNum: true}
}

func (x *axml) node(typ uint16, payload []byte) {
	le := func(v any) { binary.Write(&x.body, binary.LittleEndian, v) }
	le(typ)
	le(uint16(16))
	le(uint32(16 + len(payload)))
	le(uint32(1))          // line number
	le(uint32(0xffffffff)) // comment
	x.body.Write(payload)
}

func (x *axml) nsStart() {
	var p bytes.Buffer
	binary.Write(&p, binary.LittleEndian, []uint32{x.str("android"), x.str(androidNS)})
	x.node(0x0100, p.Bytes())
}

func (x *axml) nsEnd() {
	var p bytes.Buffer
	binary.Write(&p, binary.LittleEndian, []uint32{x.str("android"), x.str(androidNS)})
	x.node(0x0101, p.Bytes())
}

func (x *axml) start(name string, attrs ...axmlAttr) {
	var p bytes.Buffer
	le := func(v any) { binary.Write(&p, binary.LittleEndian, v) }
	le(uint32(0xffffffff))
	le(x.str(name))
	le(uint16(20)) // attribute start
	le(uint16(20)) // attribute size
	le(uint16(len(attrs)))
	le([3]uint16{})
	for _, a := range attrs {
		ns := x.str(androidNS)
		if a.name == "package" || a.name == "versionCode" || a.name == "versionName" {
			// manifest attributes: Android writes versionCode and
			// versionName in the android namespace, package without
			if a.name == "package" {
				ns = 0xffffffff
			}
		}
		le(ns)
		le(x.str(a.name))
		if a.isNum {
			le(uint32(0xffffffff))
			le(uint16(8))
			le(uint8(0))
			le(uint8(0x10)) // decimal integer
			le(uint32(a.num))
		} else {
			idx := x.str(a.str)
			le(idx)
			le(uint16(8))
			le(uint8(0))
			le(uint8(0x03)) // string
			le(idx)
		}
	}
	x.node(0x0102, p.Bytes())
}

func (x *axml) end(name string) {
	var p bytes.Buffer
	binary.Write(&p, binary.LittleEndian, []uint32{0xffffffff, x.str(name)})
	x.node(0x0103, p.Bytes())
}

func (x *axml) bytes() []byte {
	pool := stringPool(x.strs)
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, uint16(0x0003))
	binary.Write(&b, binary.LittleEndian, uint16(8))
	binary.Write(&b, binary.LittleEndian, uint32(8+len(pool)+x.body.Len()))
	b.Write(pool)
	b.Write(x.body.Bytes())
	return b.Bytes()
}

func testManifest(a testApk) []byte {
	var x axml
	x.nsStart()
	x.start("manifest", sAttr("package", a.pkg), nAttr("versionCode", a.versionCode), sAttr("versionName", a.versionName))
	x.start("uses-sdk", nAttr("minSdkVersion", 21), nAttr("targetSdkVersion", 34))
	x.end("uses-sdk")
	icon := "res/icon.png"
	if a.vectorIcon != nil {
		icon = "res/icon.xml"
	}
	x.start("application", sAttr("label", a.label), sAttr("icon", icon))
	if a.pluginAPI != "" {
		x.start("meta-data", sAttr("name", "plugin-api"), sAttr("value", a.pluginAPI))
		x.end("meta-data")
	}
	x.end("application")
	x.end("manifest")
	x.nsEnd()
	return x.bytes()
}

// testVector encodes a VectorDrawable with a 24x24 viewport and the paths.
func testVector(paths []vectorPath) []byte {
	var x axml
	x.nsStart()
	x.start("vector", sAttr("viewportWidth", "24"), sAttr("viewportHeight", "24"))
	for _, p := range paths {
		var attrs []axmlAttr
		var keys []string
		for k := range p {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			attrs = append(attrs, sAttr(k, p[k]))
		}
		x.start("path", attrs...)
		x.end("path")
	}
	x.end("vector")
	x.nsEnd()
	return x.bytes()
}

func TestWriteTestApkIsReadable(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/vns.apk"
	writeTestApk(t, path, testApk{pkg: "com.atakmap.android.vns.plugin", label: "VNS", versionCode: 7,
		versionName: "4.2 (abc)", pluginAPI: "com.atakmap.app@5.8.0.CIV"})
	info, err := readApkData(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Package != "com.atakmap.android.vns.plugin" || info.DisplayName != "VNS" || info.Revision != "7" ||
		info.Type != "plugin" || info.TakReq != "com.atakmap.app@5.8.0.CIV" || info.OsReq != 21 || info.IconPath != "res/icon.png" {
		t.Errorf("read %+v", info)
	}
	fps, err := signingCerts(path)
	if err != nil || len(fps) != 1 {
		t.Fatalf("certs %v, %v", fps, err)
	}
	other := dir + "/other.apk"
	writeTestApk(t, other, testApk{pkg: "x.y", label: "X", versionCode: 1, key: 1})
	if !signersDiffer(path, other) {
		t.Error("different keys not detected")
	}
	if data, kind, _, err := apkIcon(path, info.IconPath); err != nil || kind != "png" || len(data) == 0 {
		t.Errorf("icon: %v %s", err, kind)
	}
}
