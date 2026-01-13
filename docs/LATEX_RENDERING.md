# Laplaced LaTeX Rendering Specification v1.0

Telegram markdown doesn't support LaTeX rendering. This converter transforms common LaTeX math expressions to readable Unicode text for display in Telegram.

## Green Zone ✅ (Simple Unicode Substitution)

LaTeX commands that map 1:1 to Unicode symbols. Always safe to use.

### Greek Letters
- **Lowercase**: `\alpha` → α, `\beta` → β, `\gamma` → γ, `\delta` → δ, `\epsilon` → ε, `\zeta` → ζ, `\eta` → η, `\theta` → θ, `\iota` → ι, `\kappa` → κ, `\lambda` → λ, `\mu` → μ, `\nu` → ν, `\xi` → ξ, `\pi` → π, `\rho` → ρ, `\sigma` → σ, `\tau` → τ, `\upsilon` → υ, `\phi` → φ, `\chi` → χ, `\psi` → ψ, `\omega` → ω
- **Uppercase**: `\Gamma` → Γ, `\Delta` → Δ, `\Theta` → Θ, `\Lambda` → Λ, `\Xi` → Ξ, `\Pi` → Π, `\Sigma` → Σ, `\Phi` → Φ, `\Psi` → Ψ, `\Omega` → Ω

### Operators
- `\times` → ×, `\cdot` → ·, `\pm` → ±, `\leq` / `\le` → ≤, `\geq` / `\ge` → ≥
- `\neq` / `\ne` → ≠, `\approx` → ≈, `\infty` → ∞
- `\sum` → Σ, `\prod` → Π, `\int` → ∫

### Logic & Set Theory
- `\in` → ∈, `\notin` → ∉, `\emptyset` / `\varnothing` → ∅
- `\therefore` → ∴, `\because` → ∵
- `\forall` → ∀, `\exists` → ∃

### Arrows
- `\to` → →, `\rightarrow` → →, `\leftarrow` → ←
- `\Rightarrow` → ⇒, `\Leftarrow` → ⇐, `\Leftrightarrow` ↔
- `\iff` → ⇔, `\implies` → ⟹

### Trigonometry
- `\sin` → sin, `\cos` → cos, `\tan` → tg
- `\cot` → ctg, `\sec` → sec, `\csc` → csc
- `\arcsin` → arcsin, `\arccos` → arccos, `\arctan` → arctg

### Logarithms
- `\log` → log, `\ln` → ln, `\lg` → lg

### Geometry
- `\triangle` → △, `\angle` → ∠, `\parallel` → ∥, `\perp` → ⊥

### Vectors
- `\vec{v}` → `v⃗` (using combining character U+20D7)
- `\vec{AB}` → `AB⃗`

### Other
- `\%` → %, `\partial` → ∂, `\nabla` → ∇
- `^\circ` → ° (degrees: `25^\circ C` → `25° C`)

## Yellow Zone ⚠️ (Flattened to 1D)

LaTeX 2D constructions simplified to readable 1D text.

### Fractions
- `\frac{a}{b}` → `a/b`
- Nested: `\frac{\frac{a}{b}}{c}` → `a/b/c`

### Square Roots
- `\sqrt{x}` → `√x`
- `\sqrt{a + b}` → `√(a + b)` (adds parentheses for complex expressions)

### Subscripts & Superscripts
- `x^2` → `x²`, `x^{-1}` → `x⁻¹`, `H_2` → `H₂`
- `x^{text}` → `x^text` (removes braces for letters)
- `T_{sleep}` → `T_sleep` (Latin/Cyrillic subscripts without braces)
- `I_{\Delta}` → `I_Δ` (Unicode Greek subscripts)
- `_{10}` → `₁₀` (multi-digit subscripts)
- `_{груза}` → `_груза` (Cyrillic subscripts supported)
- Complex expressions preserve braces: `T_{i=1}` → `T_{i=1}` (has operators)

### Braces (Flattened)
- `\underbrace{FORMULA}_{TEXT}` → `FORMULA (TEXT)`
- `\overbrace{FORMULA}^{TEXT}` → `FORMULA (TEXT)`
- Example: `\underbrace{2 \times 25}_{зазоры}` → `2 × 25 (зазоры)`

### Spacing
- `\ ` (backslash-space) → ` ` (space)
- `\,` (thin space) → ` ` (space, for thousands separators: `2\,000` → `2 000`)
- `\quad` → ` `, `\qquad` → ` `
- Removed: `\!`, `\:`, `\;`

### Text Wrappers
- `\text{label}` → `label` (removes LaTeX wrapper)

## Red Zone ❌ (Not Supported)

Cannot be rendered properly in text mode. Do NOT use in prompts.

### 2D Layouts
- `\begin{matrix} ... \end{matrix}` (matrices)
- `\begin{cases} ... \end{cases}` (piecewise functions)
- `\begin{array} ... \end{array}` (arrays)

### Complex Limits
- `\lim_{x \to 0}` (limits below)
- `\int_{a}^{b}` (integral with limits)
- `\sum_{i=1}^{n}` (summation with limits - use `\sum` instead)

### Advanced Braces
- Nested braces without clear flattening: `\underbrace{\underbrace{a}_{b}}_{c}`
- Braces with complex content that loses meaning when flattened

## Implementation Notes

### Math Delimiters
- **Inline math**: `$...$`
- **Display math**: `$$...$$`, `\[...\]`, `$` on separate lines
- **Currency detection**: `$3.50` is NOT treated as math (no backslash/operators)

### Code Blocks Protected
Content inside markdown code blocks is NOT processed:
- Multi-line: ```...```
- Inline: `...`

### Unicode Primes (Inches/Feet)
- `1"` → `1″` (inches)
- `6'` → `6′` (feet/minutes)
- Automatically converted when after digits: `$1"$` → `1″`

## Examples

### Simple Formulas
- `$\alpha + \beta = \gamma$` → `α + β = γ`
- `$\sqrt{a^2 + b^2}$` → `√(a² + b²)`
- `$\frac{1}{2} + \frac{1}{3} = \frac{5}{6}$` → `1/2 + 1/3 = 5/6`

### Physics
- `$F = m \times a$` → `F = m × a`
- `$E = mc^2$` → `E = mc²`
- `$\rho = 1000 kg/m^3$` → `ρ = 1000 kg/m³`

### Finance
- `$45\,000 + 15\,000 \leq 100\,000$` → `45 000 + 15 000 ≤ 100 000`
- `$P_{total} = 80 - (6 \times 5) = 50$` → `P_total = 80 - (6 × 5) = 50`

### Engineering
- `$\sqrt{\frac{4 \times Q_{peak}}{\pi \times v_{max}}}$` → `√(4 × Q_{peak}/π × v_{max})`
- `$L = 1200 + \underbrace{2 \times 25}_{зазоры} = 1250$` → `L = 1200 + 2 × 25 (зазоры) = 1250`

### Geometry
- `$AB \parallel CD$` → `AB ∥ CD`
- `$AB \perp CD$` → `AB ⊥ CD`
- `$\triangle ABC \cong \triangle DEF$` → `△ ABC ≅ △ DEF`
- `$\angle ABC = 90^\circ$` → `∠ ABC = 90°`

### Vectors
- `$\vec{F} = m \times \vec{a}$` → `F⃗ = m × a⃗`
- `$\vec{v}_1 + \vec{v}_2$` → `v⃗₁ + v⃗₂`

### Cyrillic Subscripts
- `$\frac{\text{Вес}_{груза}}{1000}$` → `Вес_груза/1000`
- `$T_{груз}$` → `T_груз`

### Temperature
- `$25^\circ C$` → `25° C`
- `$T = -40^\circ F = -40^\circ C$` → `T = -40° F = -40° C`

## Limitations

- **No true 2D rendering**: Matrices, piecewise functions don't work
- **Line-based**: Display math becomes single line
- **Unknown commands**: Remain as `\commandname`
- **Complex integrals**: `\int_{a}^{b}` → just `∫` without limits

For complex mathematical documents, users need specialized tools with image rendering.
