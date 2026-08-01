import { memo, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import "./html-preview.css";

const iframeAllow = [
  "accelerometer 'none'", "autoplay 'none'", "camera 'none'", "display-capture 'none'",
  "encrypted-media 'none'", "fullscreen 'none'", "geolocation 'none'", "gyroscope 'none'",
  "magnetometer 'none'", "microphone 'none'", "midi 'none'", "payment 'none'",
  "picture-in-picture 'none'", "publickey-credentials-get 'none'", "screen-wake-lock 'none'",
  "usb 'none'", "web-share 'none'", "xr-spatial-tracking 'none'",
].join("; ");

export type HtmlPreviewDescriptor = {
  id: string;
  title: string;
  height: number;
  source: string;
};

export type HtmlPreviewContext = {
  previewURL: (id: string, digest: string) => string;
};

const sha256RoundConstants = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

function rotateRight(value: number, bits: number) {
  return (value >>> bits) | (value << (32 - bits));
}

function hexBytes(bytes: Uint8Array) {
  return [...bytes].map((value) => value.toString(16).padStart(2, "0")).join("");
}

function portableSha256(bytes: Uint8Array) {
  const paddedLength = Math.ceil((bytes.length + 9) / 64) * 64;
  const padded = new Uint8Array(paddedLength);
  padded.set(bytes);
  padded[bytes.length] = 0x80;
  const view = new DataView(padded.buffer);
  const bitLength = bytes.length * 8;
  view.setUint32(paddedLength - 8, Math.floor(bitLength / 0x1_0000_0000));
  view.setUint32(paddedLength - 4, bitLength >>> 0);

  const hash = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ]);
  const words = new Uint32Array(64);
  for (let offset = 0; offset < paddedLength; offset += 64) {
    for (let index = 0; index < 16; index++) words[index] = view.getUint32(offset + index * 4);
    for (let index = 16; index < 64; index++) {
      const first = words[index - 15];
      const second = words[index - 2];
      const sigma0 = rotateRight(first, 7) ^ rotateRight(first, 18) ^ (first >>> 3);
      const sigma1 = rotateRight(second, 17) ^ rotateRight(second, 19) ^ (second >>> 10);
      words[index] = (words[index - 16] + sigma0 + words[index - 7] + sigma1) >>> 0;
    }
    let [a, b, c, d, e, f, g, h] = hash;
    for (let index = 0; index < 64; index++) {
      const choice = (e & f) ^ (~e & g);
      const majority = (a & b) ^ (a & c) ^ (b & c);
      const sigma0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22);
      const sigma1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25);
      const first = (h + sigma1 + choice + sha256RoundConstants[index] + words[index]) >>> 0;
      const second = (sigma0 + majority) >>> 0;
      h = g;
      g = f;
      f = e;
      e = (d + first) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (first + second) >>> 0;
    }
    hash[0] = (hash[0] + a) >>> 0;
    hash[1] = (hash[1] + b) >>> 0;
    hash[2] = (hash[2] + c) >>> 0;
    hash[3] = (hash[3] + d) >>> 0;
    hash[4] = (hash[4] + e) >>> 0;
    hash[5] = (hash[5] + f) >>> 0;
    hash[6] = (hash[6] + g) >>> 0;
    hash[7] = (hash[7] + h) >>> 0;
  }
  return [...hash].map((value) => value.toString(16).padStart(8, "0")).join("");
}

function availableCrypto() {
  return typeof globalThis.crypto === "object" && globalThis.crypto ? globalThis.crypto : undefined;
}

async function sourceDigest(source: string) {
  const bytes = new TextEncoder().encode(source);
  const subtle = availableCrypto()?.subtle;
  if (subtle) {
    try {
      return hexBytes(new Uint8Array(await subtle.digest("SHA-256", bytes)));
    } catch {
      // Plain-HTTP and older WebViews may expose a partial crypto object.
    }
  }
  return portableSha256(bytes);
}

export const HtmlPreview = memo(function HtmlPreview({
  descriptor,
  context,
}: {
  descriptor: HtmlPreviewDescriptor;
  context: HtmlPreviewContext;
}) {
  const { t } = useTranslation();
  const [digest, setDigest] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let current = true;
    setDigest(null);
    setFailed(false);
    void sourceDigest(descriptor.source).then((value) => {
      if (current) setDigest(value);
    }, () => {
      if (current) setFailed(true);
    });
    return () => {
      current = false;
    };
  }, [descriptor.source]);

  const title = descriptor.title || t("markdown.preview.defaultTitle", { id: descriptor.id });
  if (failed) return <p className="html-preview-error" role="alert">{t("markdown.preview.startupFailure")}</p>;
  if (!digest) return null;
  return <iframe
    className="html-preview"
    data-preview-id={descriptor.id}
    title={title}
    src={context.previewURL(descriptor.id, digest)}
    height={descriptor.height}
    sandbox="allow-scripts"
    referrerPolicy="no-referrer"
    allow={iframeAllow}
    loading="eager"
  />;
});

export { iframeAllow };
