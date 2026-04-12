import btcSvg from "cryptocurrency-icons/svg/color/btc.svg?url";
import ethSvg from "cryptocurrency-icons/svg/color/eth.svg?url";
import solSvg from "cryptocurrency-icons/svg/color/sol.svg?url";
import avaxSvg from "cryptocurrency-icons/svg/color/avax.svg?url";
import dogeSvg from "cryptocurrency-icons/svg/color/doge.svg?url";
import bnbSvg from "cryptocurrency-icons/svg/color/bnb.svg?url";
import linkSvg from "cryptocurrency-icons/svg/color/link.svg?url";
import uniSvg from "cryptocurrency-icons/svg/color/uni.svg?url";
import adaSvg from "cryptocurrency-icons/svg/color/ada.svg?url";
import dotSvg from "cryptocurrency-icons/svg/color/dot.svg?url";
import xrpSvg from "cryptocurrency-icons/svg/color/xrp.svg?url";
import ltcSvg from "cryptocurrency-icons/svg/color/ltc.svg?url";
import maticSvg from "cryptocurrency-icons/svg/color/matic.svg?url";

const ICON_URLS: Record<string, string> = {
  btc: btcSvg,
  eth: ethSvg,
  sol: solSvg,
  avax: avaxSvg,
  doge: dogeSvg,
  bnb: bnbSvg,
  link: linkSvg,
  uni: uniSvg,
  ada: adaSvg,
  dot: dotSvg,
  xrp: xrpSvg,
  ltc: ltcSvg,
  matic: maticSvg,
};

const FALLBACK_COLORS: Record<string, string> = {
  btc: "#f7931a",
  eth: "#627eea",
  sol: "#9945ff",
  avax: "#e84142",
  doge: "#c2a633",
};

interface CoinIconProps {
  /** Ticker symbol — e.g. "btc", "BTC", "BTC/USD", "btc-usd" */
  symbol: string;
  size?: number;
  className?: string;
}

export function CoinIcon({ symbol, size = 24, className }: CoinIconProps) {
  const key = symbol.split(/[\/\-]/)[0].toLowerCase();
  const url = ICON_URLS[key];

  if (url) {
    return (
      <img
        src={url}
        width={size}
        height={size}
        alt={key.toUpperCase()}
        className={className}
      />
    );
  }

  const color = FALLBACK_COLORS[key] ?? "#999";
  const letter = key[0]?.toUpperCase() ?? "?";
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" className={className}>
      <circle cx="16" cy="16" r="16" fill={color} />
      <text
        x="16"
        y="21"
        textAnchor="middle"
        fontSize="14"
        fontWeight="700"
        fill="white"
        fontFamily="Arial"
      >
        {letter}
      </text>
    </svg>
  );
}

/** Resolved icon URL for use in non-React contexts (e.g. Three.js textures). */
export function getCoinIconUrl(symbol: string): string | undefined {
  const key = symbol.split(/[\/\-]/)[0].toLowerCase();
  return ICON_URLS[key];
}
