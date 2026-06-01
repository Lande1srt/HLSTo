module.exports = {
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        primary: '#00d4ff',
        secondary: '#00ff88',
        dark: {
          100: '#16213e',
          200: '#1a1a2e',
          300: '#0f0f1a'
        }
      },
      fontFamily: {
        sans: ['Inter', 'sans-serif'],
        mono: ['Roboto Mono', 'monospace']
      }
    },
  },
  plugins: [],
}
