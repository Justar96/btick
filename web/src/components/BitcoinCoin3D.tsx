import React, { Suspense, useEffect, useRef, useState } from "react";
import { Canvas, type RootState, useFrame } from "@react-three/fiber";
import { Float, useTexture } from "@react-three/drei";
import * as THREE from "three";
import btcPng from "cryptocurrency-icons/32/color/btc.png?url";
import ethPng from "cryptocurrency-icons/32/color/eth.png?url";
import solPng from "cryptocurrency-icons/32/color/sol.png?url";
import avaxPng from "cryptocurrency-icons/32/color/avax.png?url";

const REDUCED_MOTION_QUERY = "(prefers-reduced-motion: reduce)";
const MAX_RENDER_DPR = 2;
const COIN_SCALE = 0.8;

type Props = {
  size?: number;
  className?: string;
  speedRef?: React.MutableRefObject<number>;
};
type RenderMode = "webgl" | "css";
type CanvasErrorBoundaryProps = React.PropsWithChildren<{
  fallback: React.ReactNode;
}>;

interface CoinDef {
  color: string;
  rimColor: string;
  outlineColor: string;
  symbol: string;
}

const COINS: CoinDef[] = [
  { color: "#F7931A", rimColor: "#FFD700", outlineColor: "#2D1A05", symbol: "₿" },
  { color: "#627EEA", rimColor: "#8CA0F0", outlineColor: "#1A1A3E", symbol: "Ξ" },
  { color: "#9945FF", rimColor: "#C084FC", outlineColor: "#2D1050", symbol: "S" },
  { color: "#E84142", rimColor: "#F08080", outlineColor: "#3D0E0E", symbol: "A" },
];

const gradientMap = (() => {
  const data = new Uint8Array([20, 100, 180, 255]);
  const texture = new THREE.DataTexture(data, data.length, 1, THREE.RedFormat);
  texture.minFilter = THREE.NearestFilter;
  texture.magFilter = THREE.NearestFilter;
  texture.generateMipmaps = false;
  texture.needsUpdate = true;
  return texture;
})();

const COIN_THICKNESS = 0.35;
const COIN_RADIUS = 1.9;
const HALF = COIN_THICKNESS / 2;
const OUTLINE_THICKNESS = 1.035;

// Icon texture paths — order matches COINS array
const COIN_ICON_PATHS = [btcPng, ethPng, solPng, avaxPng];

function hasWebGlSupport(): boolean {
  if (typeof document === "undefined") return false;
  try {
    const canvas = document.createElement("canvas");
    return canvas.getContext("webgl") !== null;
  } catch {
    return false;
  }
}

function CssCoinFallback({ size }: { size: number }) {
  return (
    <div
      style={{
        width: size,
        height: size,
        borderRadius: "50%",
        background: "linear-gradient(135deg, #F7931A 0%, #FFD700 100%)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        fontSize: size * 0.5,
        fontWeight: 700,
        color: "white",
        textShadow: "1px 1px 2px rgba(0,0,0,0.2)",
      }}
    >
      ₿
    </div>
  );
}

function CoinMesh({ reducedMotion, onReady, speedRef }: { reducedMotion: boolean; onReady?: () => void; speedRef?: React.MutableRefObject<number> }) {
  const readyFired = useRef(false);
  const groupRef = useRef<THREE.Group>(null);
  const elapsedRef = useRef(0);
  const lastFlipRef = useRef(0);
  const lastFaceSwapRef = useRef(0);

  // Track which coin is assigned to each face
  const topCoinRef = useRef(0);
  const bottomCoinRef = useRef(1);
  const nextCoinIdxRef = useRef(2);

  // Separate materials per face — colors are set instantly when hidden
  const faceMatTop = useRef(
    new THREE.MeshToonMaterial({ color: COINS[0].color, gradientMap }),
  );
  const faceMatBottom = useRef(
    new THREE.MeshToonMaterial({ color: COINS[1].color, gradientMap }),
  );
  // Per-face ring materials so face rings match their own face, not the shared rim
  const ringMatTop = useRef(
    new THREE.MeshToonMaterial({ color: COINS[0].rimColor, gradientMap }),
  );
  const ringMatBottom = useRef(
    new THREE.MeshToonMaterial({ color: COINS[1].rimColor, gradientMap }),
  );
  const rimMat = useRef(
    new THREE.MeshToonMaterial({ color: COINS[0].rimColor, gradientMap }),
  );
  const outlineMat = useRef(
    new THREE.MeshBasicMaterial({
      color: COINS[0].outlineColor,
      side: THREE.BackSide,
    }),
  );
  // Lerp targets — only rim/outline are lerped (always visible)
  const rimTarget = useRef(new THREE.Color(COINS[0].rimColor));
  const outlineTarget = useRef(new THREE.Color(COINS[0].outlineColor));

  // Preload all coin icon textures (Suspense handles loading)
  const iconTextures = useTexture(COIN_ICON_PATHS);
  React.useMemo(() => {
    for (const tex of iconTextures) tex.colorSpace = THREE.SRGBColorSpace;
  }, [iconTextures]);

  // Mirrored variants for the bottom face (Ry(π) in rotation mirrors UVs)
  const mirroredTextures = React.useMemo(
    () =>
      iconTextures.map((tex) => {
        const m = tex.clone();
        m.wrapS = THREE.RepeatWrapping;
        m.repeat.x = -1;
        m.offset.x = 1;
        return m;
      }),
    [iconTextures],
  );

  // Icon materials per face — swap .map on flip
  const iconMatTop = useRef(
    new THREE.MeshBasicMaterial({
      map: iconTextures[0],
      transparent: true,
      depthWrite: false,
    }),
  );
  const iconMatBottom = useRef(
    new THREE.MeshBasicMaterial({
      map: mirroredTextures[1],
      transparent: true,
      depthWrite: false,
    }),
  );

  const bodyMaterials = React.useMemo(
    () => [rimMat.current, faceMatTop.current, faceMatBottom.current],
    [],
  );
  const bodyOutlineMaterials = React.useMemo(
    () => [outlineMat.current, outlineMat.current, outlineMat.current],
    [],
  );

  const baseRotation = React.useMemo(
    () => new THREE.Euler(0.12, 0.45, -0.015),
    [],
  );

  // How far past edge-on (in radians) before snapping the hidden face.
  // 0.4 rad ≈ 23° — face is well out of sight even with wobble.
  const FACE_SWAP_DELAY = 0.4;

  useFrame((_state: RootState, delta: number) => {
    if (!groupRef.current) return;

    if (!readyFired.current) {
      readyFired.current = true;
      onReady?.();
    }

    if (reducedMotion) {
      groupRef.current.rotation.copy(baseRotation);
      return;
    }

    const speed = speedRef?.current ?? 1;
    elapsedRef.current += delta * speed;
    const time = elapsedRef.current;
    const yRot = baseRotation.y + time * 0.5;

    groupRef.current.rotation.x =
      baseRotation.x + Math.sin(time * 0.8) * 0.08;
    groupRef.current.rotation.y = yRot;
    groupRef.current.rotation.z =
      baseRotation.z + Math.cos(time * 0.6) * 0.06;

    // Use yRot directly (not totalY) so detection aligns with the
    // actual visual edge-on, which depends on baseRotation.y.
    // Visual edge-on: cos(yRot) = 0 → yRot = π/2 + nπ

    // --- Event 1: visual edge-on — start rim/outline color blend ---
    const flipCount = Math.floor((yRot + Math.PI / 2) / Math.PI);
    if (flipCount > lastFlipRef.current) {
      lastFlipRef.current = flipCount;

      if (flipCount % 2 === 1) {
        // Bottom is becoming visible
        const visible = COINS[bottomCoinRef.current];
        rimTarget.current.set(visible.rimColor);
        outlineTarget.current.set(visible.outlineColor);
      } else {
        // Top is becoming visible
        const visible = COINS[topCoinRef.current];
        rimTarget.current.set(visible.rimColor);
        outlineTarget.current.set(visible.outlineColor);
      }
    }

    // --- Event 2: delayed past visual edge-on — snap hidden face ---
    // Fires FACE_SWAP_DELAY rad after edge-on so the face is fully
    // out of sight even with wobble. All face elements update at once.
    const faceSwapCount = Math.floor(
      (yRot + Math.PI / 2 - FACE_SWAP_DELAY) / Math.PI,
    );
    if (faceSwapCount > lastFaceSwapRef.current) {
      lastFaceSwapRef.current = faceSwapCount;

      const nextCoin = COINS[nextCoinIdxRef.current % COINS.length];

      const nextIdx = nextCoinIdxRef.current % COINS.length;

      if (faceSwapCount % 2 === 1) {
        // Top is fully hidden — snap it to the next coin
        topCoinRef.current = nextIdx;
        faceMatTop.current.color.set(nextCoin.color);
        ringMatTop.current.color.set(nextCoin.rimColor);
        iconMatTop.current.map = iconTextures[nextIdx];
        iconMatTop.current.needsUpdate = true;
      } else {
        // Bottom is fully hidden — snap it to the next coin
        bottomCoinRef.current = nextIdx;
        faceMatBottom.current.color.set(nextCoin.color);
        ringMatBottom.current.color.set(nextCoin.rimColor);
        iconMatBottom.current.map = mirroredTextures[nextIdx];
        iconMatBottom.current.needsUpdate = true;
      }

      nextCoinIdxRef.current++;
    }

    // Fast rim/outline lerp — completes in ~0.2s so it's done before
    // the new face becomes prominent after the edge-on moment.
    const rimLerp = 1 - Math.exp(-15 * delta);
    rimMat.current.color.lerp(rimTarget.current, rimLerp);
    outlineMat.current.color.lerp(outlineTarget.current, rimLerp);
  });

  return (
    <group
      ref={groupRef}
      position={[0, 0, 0]}
      scale={COIN_SCALE}
      rotation={[baseRotation.x, baseRotation.y, baseRotation.z]}
    >
      <group rotation={[Math.PI / 2, 0, 0]}>
        {/* Main Coin Body */}
        <group>
          <mesh castShadow receiveShadow material={bodyMaterials}>
            <cylinderGeometry
              args={[COIN_RADIUS, COIN_RADIUS, COIN_THICKNESS, 64]}
            />
          </mesh>
          <mesh material={bodyOutlineMaterials}>
            <cylinderGeometry
              args={[
                COIN_RADIUS * OUTLINE_THICKNESS,
                COIN_RADIUS * OUTLINE_THICKNESS,
                COIN_THICKNESS * 1.12,
                64,
              ]}
            />
          </mesh>
        </group>

        {/* Edge ridges */}
        {[-0.12, -0.04, 0.04, 0.12].map((y) => (
          <group key={y} position={[0, y, 0]} rotation={[Math.PI / 2, 0, 0]}>
            <mesh material={rimMat.current}>
              <torusGeometry args={[COIN_RADIUS + 0.01, 0.025, 8, 64]} />
            </mesh>
          </group>
        ))}

        {/* Rim — top and bottom edge */}
        <group position={[0, HALF, 0]} rotation={[Math.PI / 2, 0, 0]}>
          <mesh material={rimMat.current}>
            <torusGeometry args={[COIN_RADIUS - 0.05, 0.08, 16, 64]} />
          </mesh>
          <mesh material={outlineMat.current}>
            <torusGeometry args={[COIN_RADIUS - 0.05, 0.08 * 1.3, 16, 64]} />
          </mesh>
        </group>

        <group position={[0, -HALF, 0]} rotation={[Math.PI / 2, 0, 0]}>
          <mesh material={rimMat.current}>
            <torusGeometry args={[COIN_RADIUS - 0.05, 0.08, 16, 64]} />
          </mesh>
          <mesh material={outlineMat.current}>
            <torusGeometry args={[COIN_RADIUS - 0.05, 0.08 * 1.3, 16, 64]} />
          </mesh>
        </group>

        {/* Face ring detail — per-face materials so they match their own coin */}
        <mesh
          position={[0, HALF + 0.005, 0]}
          rotation={[-Math.PI / 2, 0, 0]}
          material={ringMatTop.current}
        >
          <ringGeometry args={[1.4, 1.55, 64]} />
        </mesh>

        <mesh
          position={[0, -(HALF + 0.005), 0]}
          rotation={[Math.PI / 2, 0, 0]}
          material={ringMatBottom.current}
        >
          <ringGeometry args={[1.4, 1.55, 64]} />
        </mesh>

        {/* Top face icon */}
        <mesh
          position={[0, HALF + 0.02, 0]}
          rotation={[-Math.PI / 2, 0, -0.25]}
          material={iconMatTop.current}
        >
          <circleGeometry args={[1.1, 64]} />
        </mesh>

        {/* Bottom face icon — Ry(π) compensates for outer group rotation,
             mirrored texture corrects the UV flip */}
        <mesh
          position={[0, -(HALF + 0.02), 0]}
          rotation={[-Math.PI / 2, Math.PI, -0.25]}
          material={iconMatBottom.current}
        >
          <circleGeometry args={[1.1, 64]} />
        </mesh>
      </group>
    </group>
  );
}

class CanvasErrorBoundary extends React.Component<
  CanvasErrorBoundaryProps,
  { hasError: boolean }
> {
  constructor(props: CanvasErrorBoundaryProps) {
    super(props);
    this.state = { hasError: false };
  }

  static getDerivedStateFromError() {
    return { hasError: true };
  }

  override componentDidCatch() {
    this.setState({ hasError: true });
  }

  override render() {
    if (this.state.hasError) {
      return this.props.fallback ?? null;
    }
    return this.props.children;
  }
}

export function BitcoinCoin3D({ size = 64, className, speedRef }: Props) {
  const [renderMode, setRenderMode] = useState<RenderMode>(() =>
    hasWebGlSupport() ? "webgl" : "css",
  );
  const [reducedMotion, setReducedMotion] = useState(false);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    if (renderMode !== "webgl") return;
    if (typeof window.matchMedia !== "function") return;

    const mediaQuery = window.matchMedia(REDUCED_MOTION_QUERY);
    const updateReducedMotion = () => setReducedMotion(mediaQuery.matches);

    updateReducedMotion();
    mediaQuery.addEventListener?.("change", updateReducedMotion);

    return () => {
      mediaQuery.removeEventListener?.("change", updateReducedMotion);
    };
  }, [renderMode]);

  if (renderMode === "css") {
    return <CssCoinFallback size={size} />;
  }

  return (
    <CanvasErrorBoundary fallback={<CssCoinFallback size={size} />}>
      <div
        aria-hidden="true"
        className={className}
        style={{
          width: className ? "100%" : `${size * 2}px`,
          height: className ? "100%" : `${size * 2}px`,
          display: "block",
          pointerEvents: "none",
          opacity: ready ? 1 : 0,
          transition: "opacity 0.4s ease",
        }}
      >
        <Canvas
          dpr={[1, MAX_RENDER_DPR]}
          gl={{ antialias: true, alpha: true }}
          camera={{ position: [0, 0, 6.15], fov: 50 }}
          onCreated={({ gl }) => {
            gl.outputColorSpace = THREE.SRGBColorSpace;
            gl.setClearColor(0x000000, 0);
          }}
          onError={() => setRenderMode("css")}
          style={{ width: "100%", height: "100%" }}
        >
          <ambientLight intensity={1.0} color="#ffffff" />
          <directionalLight
            position={[5, 10, 10]}
            intensity={3.5}
            color="#ffffff"
          />
          <directionalLight
            position={[-5, 5, -10]}
            intensity={2.0}
            color="#ffdeaa"
          />
          <spotLight
            position={[0, 10, 0]}
            intensity={1.5}
            penumbra={1}
            color="#ffffff"
          />

          <Suspense fallback={null}>
            <Float rotationIntensity={0.05} floatIntensity={0.24} speed={0.9}>
              <CoinMesh reducedMotion={reducedMotion} onReady={() => setReady(true)} speedRef={speedRef} />
            </Float>
          </Suspense>
        </Canvas>
      </div>
    </CanvasErrorBoundary>
  );
}
