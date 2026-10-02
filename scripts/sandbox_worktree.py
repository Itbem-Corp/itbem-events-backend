"""Bounded source binding shared with the Go sandbox worker, version 1."""
import hashlib
import os
import pathlib
import stat
import struct

EXCLUDED = {'.git', 'node_modules', '.next', '.venv', 'venv'}


def open_beneath(source, relative):
    directory = os.open(source, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        parts = pathlib.PurePosixPath(relative).parts
        for part in parts[:-1]:
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=directory)
            os.close(directory)
            directory = child
        return os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
    finally:
        os.close(directory)


def credential_name(name):
    name = name.lower()
    if name in {'.env.example', '.env.sample', '.env.template'}:
        return False
    return name == '.env' or name.startswith('.env.') or name in {'.aws', '.ssh', '.local', '.codex', '.config', 'credentials', 'id_rsa', 'id_ed25519'} or name.endswith('.key')


def snapshot_worktree(source, destination=None):
    source = pathlib.Path(source).absolute()
    if source.is_symlink() or not source.is_dir():
        raise RuntimeError('worktree root must be a real directory')
    paths = []
    def walk_error(error):
        raise error
    for root, dirs, names in os.walk(source, followlinks=False, onerror=walk_error):
        dirs[:] = [name for name in dirs if name not in EXCLUDED]
        for name in dirs:
            if credential_name(name):
                raise RuntimeError('credential material is not allowed in a sandbox worktree')
            if (pathlib.Path(root)/name).is_symlink():
                raise RuntimeError('worktree contains a symlink')
        for name in names:
            if name in EXCLUDED:
                continue
            if credential_name(name):
                raise RuntimeError('credential material is not allowed in a sandbox worktree')
            relative = (pathlib.Path(root)/name).relative_to(source)
            if len(relative.parts) > 32:
                raise RuntimeError('invalid worktree path')
            paths.append(relative.as_posix())
            if len(paths) > 4096:
                raise RuntimeError('worktree file count exceeds transfer bound')
    paths.sort(key=lambda name: name.encode('utf-8'))
    digest = hashlib.sha256(b'itbem-sandbox-source-v1\n')
    total = 0
    if destination is not None:
        destination = pathlib.Path(destination)
        destination.mkdir(parents=True, exist_ok=False)
    for relative in paths:
        path = source/relative
        before = path.lstat()
        if not stat.S_ISREG(before.st_mode):
            raise RuntimeError('worktree contains a non-regular file')
        if before.st_size > 8*1024*1024:
            raise RuntimeError('oversized worktree file')
        total += before.st_size
        if total > 64*1024*1024:
            raise RuntimeError('worktree bytes exceed transfer bound')
        descriptor = open_beneath(source, relative)
        with os.fdopen(descriptor, 'rb') as handle:
            opened = os.fstat(handle.fileno())
            if (opened.st_dev, opened.st_ino) != (before.st_dev, before.st_ino):
                raise RuntimeError('worktree changed before hashing')
            content = handle.read(8*1024*1024+1)
            after = os.fstat(handle.fileno())
        current = path.lstat()
        if len(content) != before.st_size or (after.st_size, after.st_mtime_ns) != (before.st_size, before.st_mtime_ns) or (current.st_dev, current.st_ino) != (before.st_dev, before.st_ino):
            raise RuntimeError('worktree changed while hashing')
        encoded = relative.encode('utf-8')
        digest.update(struct.pack('>Q', len(encoded)))
        digest.update(encoded)
        digest.update(bytes([bool(before.st_mode & 0o111)]))
        digest.update(struct.pack('>Q', len(content)))
        digest.update(hashlib.sha256(content).digest())
        if destination is not None:
            target = destination/relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
            target.chmod(before.st_mode & 0o777)
    return 'sha256:'+digest.hexdigest(), len(paths), total
