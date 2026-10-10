// Terrarium "Habitat" mark: a glass enclosure holding a connected agent network. Same artwork as
// app/icon.svg (the browser tab icon).
export function Logo({ size = 20 }: { size?: number }) {
  return (
    <svg viewBox="0 0 128 128" width={size} height={size} fill="none" aria-hidden>
      <path
        d="M60 15 Q64 13 68 15 L101 34 Q105 36 105 41 V86 Q105 91 101 93 L68 112 Q64 114 60 112 L27 93 Q23 91 23 86 V41 Q23 36 27 34 Z"
        stroke="#A2E8BF"
        strokeWidth="5.5"
        strokeLinejoin="round"
      />
      <path d="M43 49 L64 65 L85 49 M64 65 V89" stroke="#EAF5EE" strokeWidth="5.5" strokeLinecap="round" strokeLinejoin="round" />
      <circle cx="43" cy="49" r="7.5" fill="#EAF5EE" />
      <circle cx="85" cy="49" r="7.5" fill="#EAF5EE" />
      <circle cx="64" cy="89" r="7.5" fill="#EAF5EE" />
      <circle cx="64" cy="65" r="10" fill="#A2E8BF" />
    </svg>
  );
}
