package client

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
	"strings"
	"sync"
)

var ErrGenerationRequired = errors.New("workspace requires explicit generation adoption before writing; stop existing writers and run workspace migration")

type generationClient struct {
	Client
	rdb        *redis.Client
	key        string
	mu         sync.Mutex
	generation string
}

func guardClient(c Client, rdb *redis.Client, key string) Client {
	return &generationClient{Client: c, rdb: rdb, key: key}
}
func (c *generationClient) bind(ctx context.Context, writing bool) (context.Context, error) {
	if expected, ok := ctx.Value(workspaceGenerationKey{}).(string); ok {
		actual, err := c.rdb.Get(ctx, "afs:{"+c.key+"}:generation").Result()
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, ErrWorkspaceChanged
		}
		return ctx, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	actual, err := c.rdb.Get(ctx, "afs:{"+c.key+"}:generation").Result()
	if errors.Is(err, redis.Nil) {
		if c.generation != "" {
			return nil, ErrWorkspaceChanged
		}
		if writing {
			exists, e := c.rdb.Exists(ctx, "afs:{"+c.key+"}:workspace:meta").Result()
			if e != nil {
				return nil, e
			}
			if exists > 0 {
				return nil, ErrGenerationRequired
			}
		}
		return ctx, nil
	}
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(actual, "g_") {
		return nil, ErrWorkspaceChanged
	}
	if c.generation == "" {
		c.generation = actual
	}
	if actual != c.generation {
		return nil, ErrWorkspaceChanged
	}
	return WithWorkspaceGeneration(ctx, c.generation), nil
}
func (c *generationClient) Stat(ctx context.Context, path string) (r0 *StatResult, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Stat(ctx, path)
}
func (c *generationClient) StatInode(ctx context.Context, inode uint64) (r0 *StatResult, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.StatInode(ctx, inode)
}
func (c *generationClient) Cat(ctx context.Context, path string) (r0 []byte, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Cat(ctx, path)
}
func (c *generationClient) Echo(ctx context.Context, path string, data []byte) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Echo(ctx, path, data)
}
func (c *generationClient) EchoCreate(ctx context.Context, path string, data []byte, mode uint32) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.EchoCreate(ctx, path, data, mode)
}
func (c *generationClient) CreateFile(ctx context.Context, path string, mode uint32, exclusive bool) (r0 *StatResult, r1 bool, err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.CreateFile(ctx, path, mode, exclusive)
}
func (c *generationClient) EchoAppend(ctx context.Context, path string, data []byte) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.EchoAppend(ctx, path, data)
}
func (c *generationClient) Touch(ctx context.Context, path string) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Touch(ctx, path)
}
func (c *generationClient) ReadInodeAt(ctx context.Context, inode uint64, off int64, size int) (r0 []byte, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.ReadInodeAt(ctx, inode, off, size)
}
func (c *generationClient) WriteInodeAt(ctx context.Context, inode uint64, data []byte, off int64) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.WriteInodeAt(ctx, inode, data, off)
}
func (c *generationClient) WriteInodeAtPath(ctx context.Context, inode uint64, path string, data []byte, off int64) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.WriteInodeAtPath(ctx, inode, path, data, off)
}
func (c *generationClient) TruncateInode(ctx context.Context, inode uint64, size int64) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.TruncateInode(ctx, inode, size)
}
func (c *generationClient) TruncateInodeAtPath(ctx context.Context, inode uint64, path string, size int64) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.TruncateInodeAtPath(ctx, inode, path, size)
}
func (c *generationClient) Getlk(ctx context.Context, inode uint64, handleID string, lk *FileLock) (r0 *FileLock, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Getlk(ctx, inode, handleID, lk)
}
func (c *generationClient) Setlk(ctx context.Context, inode uint64, handleID string, lk *FileLock, wait bool) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Setlk(ctx, inode, handleID, lk, wait)
}
func (c *generationClient) UnlockAll(ctx context.Context, inode uint64, handleID string) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.UnlockAll(ctx, inode, handleID)
}
func (c *generationClient) Mkdir(ctx context.Context, path string) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Mkdir(ctx, path)
}
func (c *generationClient) MkdirMode(ctx context.Context, path string, mode uint32) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.MkdirMode(ctx, path, mode)
}
func (c *generationClient) Rm(ctx context.Context, path string) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Rm(ctx, path)
}
func (c *generationClient) Ls(ctx context.Context, path string) (r0 []string, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Ls(ctx, path)
}
func (c *generationClient) LsLong(ctx context.Context, path string) (r0 []LsEntry, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.LsLong(ctx, path)
}
func (c *generationClient) Rename(ctx context.Context, src string, dst string, flags uint32) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Rename(ctx, src, dst, flags)
}
func (c *generationClient) Mv(ctx context.Context, src string, dst string) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Mv(ctx, src, dst)
}
func (c *generationClient) Ln(ctx context.Context, target string, linkpath string) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Ln(ctx, target, linkpath)
}
func (c *generationClient) Readlink(ctx context.Context, path string) (r0 string, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Readlink(ctx, path)
}
func (c *generationClient) Chmod(ctx context.Context, path string, mode uint32) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Chmod(ctx, path, mode)
}
func (c *generationClient) Chown(ctx context.Context, path string, uid uint32, gid uint32) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Chown(ctx, path, uid, gid)
}
func (c *generationClient) Truncate(ctx context.Context, path string, size int64) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Truncate(ctx, path, size)
}
func (c *generationClient) Utimens(ctx context.Context, path string, atimeMs int64, mtimeMs int64) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Utimens(ctx, path, atimeMs, mtimeMs)
}
func (c *generationClient) SetAttrs(ctx context.Context, path string, upd AttrUpdate) (err error) {
	if upd.IsEmpty() {
		return nil
	}
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.SetAttrs(ctx, path, upd)
}
func (c *generationClient) Info(ctx context.Context) (r0 *InfoResult, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Info(ctx)
}
func (c *generationClient) Head(ctx context.Context, path string, n int) (r0 string, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Head(ctx, path, n)
}
func (c *generationClient) Tail(ctx context.Context, path string, n int) (r0 string, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Tail(ctx, path, n)
}
func (c *generationClient) Lines(ctx context.Context, path string, start int, end int) (r0 string, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Lines(ctx, path, start, end)
}
func (c *generationClient) Wc(ctx context.Context, path string) (r0 *WcResult, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Wc(ctx, path)
}
func (c *generationClient) Insert(ctx context.Context, path string, afterLine int, content string) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Insert(ctx, path, afterLine, content)
}
func (c *generationClient) Replace(ctx context.Context, path string, old string, new string, all bool) (r0 int64, err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Replace(ctx, path, old, new, all)
}
func (c *generationClient) DeleteLines(ctx context.Context, path string, start int, end int) (r0 int64, err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.DeleteLines(ctx, path, start, end)
}
func (c *generationClient) Cp(ctx context.Context, src string, dst string, recursive bool) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.Cp(ctx, src, dst, recursive)
}
func (c *generationClient) Tree(ctx context.Context, path string, maxDepth int) (r0 []TreeEntry, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Tree(ctx, path, maxDepth)
}
func (c *generationClient) Find(ctx context.Context, path string, pattern string, typeFilter string) (r0 []string, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Find(ctx, path, pattern, typeFilter)
}
func (c *generationClient) Grep(ctx context.Context, path string, pattern string, nocase bool) (r0 []GrepMatch, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.Grep(ctx, path, pattern, nocase)
}
func (c *generationClient) WriteChunks(ctx context.Context, path string, chunks map[int][]byte, chunkSize int, newSize int64, hashes []string) (err error) {
	ctx, err = c.bind(ctx, true)
	if err != nil {
		return
	}
	return c.Client.WriteChunks(ctx, path, chunks, chunkSize, newSize, hashes)
}
func (c *generationClient) ReadChunks(ctx context.Context, path string, indices []int, chunkSize int) (r0 map[int][]byte, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.ReadChunks(ctx, path, indices, chunkSize)
}
func (c *generationClient) ChunkMeta(ctx context.Context, path string) (r0 int, r1 []string, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.ChunkMeta(ctx, path)
}
func (c *generationClient) ReadChangeStream(ctx context.Context, lastID string, count int64) (r0 []ChangeStreamEntry, err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			_, err = c.bind(ctx, false)
		}
	}()
	return c.Client.ReadChangeStream(ctx, lastID, count)
}
func (c *generationClient) SubscribeInvalidationsWithReconnect(ctx context.Context, handler func(InvalidateEvent), onReconnect func()) (err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	return c.Client.SubscribeInvalidationsWithReconnect(ctx, handler, onReconnect)
}
func (c *generationClient) SubscribeInvalidations(ctx context.Context, handler func(InvalidateEvent)) (err error) {
	ctx, err = c.bind(ctx, false)
	if err != nil {
		return
	}
	return c.Client.SubscribeInvalidations(ctx, handler)
}
