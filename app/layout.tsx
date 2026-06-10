import type { Metadata, Viewport } from "next";
import { Geist } from "next/font/google";
import "./globals.css";

const geist = Geist({ subsets: ["latin"], variable: "--font-geist" });

export const metadata: Metadata = {
  title: "LiveTV — Watch Bangla, Sports & More",
  description: "Free live streaming of Bangla, Hindi, Sports, News, Movies and more channels.",
};

export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
  minimumScale: 1,
  maximumScale: 5,
  themeColor: '#181818',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${geist.variable} h-full dark`}>
      <body className="h-full overflow-hidden bg-[#0d0d0d] text-white antialiased">
        {children}
      </body>
    </html>
  );
}
