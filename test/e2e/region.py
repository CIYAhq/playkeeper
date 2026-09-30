"""Reads blocks from a world the way Minecraft saves it: Anvil region files
holding each chunk's NBT. Checks use it to see what a backup holds without
asking a server."""
import gzip
import io
import struct
import zlib

NUMBERS = {1: ">b", 2: ">h", 3: ">i", 4: ">q", 5: ">f", 6: ">d"}


def _tag(f, kind):
    if kind in NUMBERS:
        return struct.unpack(NUMBERS[kind], f.read(struct.calcsize(NUMBERS[kind])))[0]
    if kind == 7:
        return f.read(struct.unpack(">i", f.read(4))[0])
    if kind == 8:
        return f.read(struct.unpack(">H", f.read(2))[0]).decode("utf-8", "replace")
    if kind == 9:
        item, n = struct.unpack(">bi", f.read(5))
        return [_tag(f, item) for _ in range(n)]
    if kind == 10:
        out = {}
        while (item := f.read(1)[0]) != 0:
            name = _tag(f, 8)
            out[name] = _tag(f, item)
        return out
    if kind == 11:
        n = struct.unpack(">i", f.read(4))[0]
        return list(struct.unpack(f">{n}i", f.read(4 * n)))
    if kind == 12:
        n = struct.unpack(">i", f.read(4))[0]
        return list(struct.unpack(f">{n}q", f.read(8 * n)))
    raise ValueError(f"unknown NBT tag type {kind}")


def region_name(x, z):
    """The region file that holds the block at x z."""
    return f"r.{x >> 9}.{z >> 9}.mca"


def chunk(region, x, z):
    """The NBT of the chunk holding the block at x z, read from a region
    file's bytes, or None when the file has no such chunk."""
    entry = 4 * ((x >> 4 & 31) + (z >> 4 & 31) * 32)
    start = int.from_bytes(region[entry:entry + 3], "big") * 4096
    if start == 0:
        return None
    length, compression = struct.unpack(">iB", region[start:start + 5])
    data = region[start + 5:start + 4 + length]
    if compression == 1:
        data = gzip.decompress(data)
    elif compression == 2:
        data = zlib.decompress(data)
    elif compression != 3:
        raise ValueError(f"chunk compression {compression} is not read here")
    f = io.BytesIO(data)
    if f.read(1)[0] != 10:
        raise ValueError("a chunk's NBT must start with a compound")
    _tag(f, 8)
    return _tag(f, 10)


def block(nbt, x, y, z):
    """The name of the block at x y z in a chunk's NBT, and its block entity
    (a sign's text, say) or None."""
    name = None
    section = next((s for s in nbt.get("sections", []) if s["Y"] == y >> 4 and "block_states" in s), None)
    if section:
        palette = section["block_states"]["palette"]
        index = 0
        if len(palette) > 1:
            # Each block's palette index takes the same number of bits, packed
            # into 64-bit words with none split between two.
            bits = max(4, (len(palette) - 1).bit_length())
            per_word = 64 // bits
            i = (y & 15) * 256 + (z & 15) * 16 + (x & 15)
            word = section["block_states"]["data"][i // per_word] % (1 << 64)
            index = (word >> (i % per_word * bits)) % (1 << bits)
        name = palette[index]["Name"]
    entity = next((e for e in nbt.get("block_entities", []) if (e["x"], e["y"], e["z"]) == (x, y, z)), None)
    return name, entity
