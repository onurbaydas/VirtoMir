package virt

import (
	"bytes"
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"os"
	"strings"
)

// sha512Crypt is glibc's "$6$" password hash (Ulrich Drepper, "Unix crypt using SHA-256 and
// SHA-512", default 5000 rounds), the format /etc/shadow uses on Debian, Ubuntu and Alpine.
func sha512Crypt(key, salt []byte) string {
	if len(salt) > 16 {
		salt = salt[:16]
	}
	b := sha512.New()
	b.Write(key)
	b.Write(salt)
	b.Write(key)
	sumB := b.Sum(nil)

	a := sha512.New()
	a.Write(key)
	a.Write(salt)
	for n := len(key); n > 0; n -= 64 {
		a.Write(sumB[:min(n, 64)])
	}
	for n := len(key); n > 0; n >>= 1 {
		if n&1 != 0 {
			a.Write(sumB)
		} else {
			a.Write(key)
		}
	}
	sumA := a.Sum(nil)

	dp := sha512.New()
	for range key {
		dp.Write(key)
	}
	p := repeatTo(dp.Sum(nil), len(key))

	ds := sha512.New()
	for range 16 + int(sumA[0]) {
		ds.Write(salt)
	}
	s := repeatTo(ds.Sum(nil), len(salt))

	c := sumA
	for i := range 5000 {
		h := sha512.New()
		if i&1 != 0 {
			h.Write(p)
		} else {
			h.Write(c)
		}
		if i%3 != 0 {
			h.Write(s)
		}
		if i%7 != 0 {
			h.Write(p)
		}
		if i&1 != 0 {
			h.Write(c)
		} else {
			h.Write(p)
		}
		c = h.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$6$")
	out.Write(salt)
	out.WriteByte('$')
	order := [][3]int{{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13},
		{56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41}}
	for _, o := range order {
		b64(&out, uint(c[o[0]])<<16|uint(c[o[1]])<<8|uint(c[o[2]]), 4)
	}
	b64(&out, uint(c[63]), 2)
	return out.String()
}

func repeatTo(sum []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, sum[:min(n-len(out), len(sum))]...)
	}
	return out
}

const cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func b64(w *strings.Builder, v uint, n int) {
	for range n {
		w.WriteByte(cryptAlphabet[v&0x3f])
		v >>= 6
	}
}

// HashPassword returns a glibc-style $6$ SHA-512 password hash for the given
// plaintext password, using a random 16-character salt. The result is in the
// format cloud-init expects for its hashed_passwd field.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	for i := range salt {
		salt[i] = cryptAlphabet[int(salt[i])%len(cryptAlphabet)]
	}
	return sha512Crypt([]byte(password), salt), nil
}

// IsCryptHash reports whether s looks like a glibc crypt password hash
// ($1$ MD5, $5$ SHA-256, $6$ SHA-512).
func IsCryptHash(s string) bool {
	return strings.HasPrefix(s, "$1$") || strings.HasPrefix(s, "$5$") || strings.HasPrefix(s, "$6$")
}

// setRootPassword writes root's password hash into etc/shadow under root.
func setRootPassword(root *os.Root, password string) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	for i := range salt {
		salt[i] = cryptAlphabet[int(salt[i])%len(cryptAlphabet)]
	}
	hash := sha512Crypt([]byte(password), salt)

	const path = "etc/shadow"
	raw, err := root.ReadFile(path)
	if err != nil {
		return err
	}
	lines := bytes.Split(raw, []byte("\n"))
	found := false
	for i, line := range lines {
		f := bytes.SplitN(line, []byte(":"), 3)
		if len(f) == 3 && string(f[0]) == "root" {
			lines[i] = []byte("root:" + hash + ":" + string(f[2]))
			found = true
		}
	}
	if !found {
		return errors.New("the image's /etc/shadow has no root entry")
	}
	info, err := root.Stat(path)
	if err != nil {
		return err
	}
	return root.WriteFile(path, bytes.Join(lines, []byte("\n")), info.Mode().Perm())
}
