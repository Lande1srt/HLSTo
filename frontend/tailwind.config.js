module.exports = {
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        primary: '#3498db',
        secondary: '#34495e',
        dark: {
          100: '#2d2d44',
          200: '#1a1a2e',
          300: '#16162a',
          400: '#121220',
          500: '#0d0d18'
        },
        text: '#2c3e50',
        card: '#ffffff'
      },
      fontFamily: {
        sans: ['sans-serif']
      },
      boxShadow: {
        'flat': '0 2px 4px rgba(0, 0, 0, 0.1)'
      }
    },
  },
  plugins: [],
}
