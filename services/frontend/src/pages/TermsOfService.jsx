import React from 'react'

const TermsOfService = () => {
    return (
        <div className="min-h-screen bg-background flex flex-col justify-center py-12 sm:px-6 lg:px-8 font-sans">
            <div className="sm:mx-auto sm:w-full sm:max-w-4xl">
                <h2 className="mt-6 text-center text-3xl text-text font-serif">
                    Kullanım Koşulları
                </h2>
                <div className="mt-8 bg-surface py-8 px-4 shadow-subtle sm:rounded-lg sm:px-10">
                    <div className="max-w-none text-text-secondary">
                        <p className="text-sm text-text-tertiary mb-4 font-sans">Son Güncelleme: 12 Mayıs 2026 · Sürüm: 1.0</p>

                        <div className="mb-6 border-l-4 border-accent pl-4 py-3 text-text-secondary font-sans text-sm bg-background/40">
                            <strong className="text-text font-medium block mb-1">Beta / PoC Bilgilendirmesi</strong>
                            Syncrun şu anda kapalı beta (Proof of Concept) aşamasındadır ve sınırlı sayıda pilot kullanıcı ile test edilmektedir. Hizmet, kararlı sürüm öncesi olduğundan veri kaybı, hizmet kesintisi ve yapay zekâ kaynaklı hatalı çıktı riski mevcuttur; üretim veya ticari amaçlı kullanım önerilmez. Pilot katılımcılarımızla ayrıca <a href="/pilot-sozlesmesi" className="text-accent hover:text-accent-hover">Pilot Kullanıcı Sözleşmesi</a> imzalanmaktadır.
                        </div>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">1. Taraflar ve Kabul</h3>
                        <p className="font-sans">
                            İşbu Kullanım Koşulları ("Koşullar"), Syncrun adı altında pilot hizmet sunan eşit oranda ortak veri sorumluları <strong>Faik İhsan Südüpak</strong> ve <strong>Kerem Yoldaş</strong> (birlikte "Syncrun", "biz", "bizim") ile Syncrun hizmetlerini ("Hizmet") kullanan gerçek veya tüzel kişi ("Kullanıcı", "siz") arasında akdedilmiştir. Hizmete kayıt olarak veya kullanarak bu Koşulları, Gizlilik Politikası'nı, KVKK Aydınlatma Metni'ni ve geçerliyse Pilot Kullanıcı Sözleşmesi'ni okuduğunuzu, anladığınızı ve kabul ettiğinizi beyan edersiniz. Kabul etmiyorsanız Hizmet'i kullanmayınız.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">2. Hizmetin Tanımı</h3>
                        <p className="font-sans">
                            Syncrun; Google Takvim ile entegre çalışan, toplantılara katılım, ses kaydı, yapay zekâ destekli transkripsiyon, özet, karar ve aksiyon çıkarımı ile toplantı öncesi bilgilendirme (pre-meeting brief) sunan bir Yazılım-Hizmet (SaaS) platformudur. Hizmet, Web Paneli, Masaüstü Kaydedici uygulaması ve isteğe bağlı toplantı botu bileşenlerinden oluşur.
                        </p>
                        <p className="mt-2 font-sans">
                            Hizmet "olduğu gibi" sunulmaktadır; özellikler, yapay zekâ modelleri ve entegrasyonlar zaman içinde değişebilir, askıya alınabilir veya kaldırılabilir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">3. Hesap Oluşturma ve Güvenlik</h3>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li>18 yaşından küçük kişiler Hizmet'i kullanamaz.</li>
                            <li>Verdiğiniz bilgilerin doğru, güncel ve eksiksiz olduğunu taahhüt edersiniz.</li>
                            <li>Hesap erişim bilgilerinizin gizliliğinden ve hesabınızdan yapılan tüm işlemlerden siz sorumlusunuz.</li>
                            <li>Yetkisiz bir erişim fark ederseniz derhal <strong>support@syncrun.net</strong> adresine bildirmekle yükümlüsünüz.</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">4. Toplantı Kaydı ve Katılımcı Rızası</h3>
                        <p className="font-sans">
                            Hizmet, toplantılarınızın sesini kaydeder ve metne döker. Türkiye Cumhuriyeti mevzuatı, 6698 sayılı Kişisel Verilerin Korunması Kanunu (KVKK) ve uluslararası gizlilik mevzuatı kapsamında <strong>tüm toplantı katılımcılarını kayıt yapıldığı konusunda bilgilendirmek ve gerekli açık rızalarını almak Kullanıcı'nın sorumluluğundadır.</strong>
                        </p>
                        <p className="mt-2 font-sans">
                            Syncrun, katılımcıların rızasının alınmamasından doğacak hukuki, idari veya cezai sorumluluktan münhasıran Kullanıcı'nın sorumlu olduğunu kabul eder. Üçüncü kişilerden gelen şikâyet, talep veya yaptırımlar nedeniyle Syncrun'ın uğrayacağı her türlü zarar Kullanıcı tarafından karşılanır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">5. Kullanıcı Yükümlülükleri ve Yasak Kullanımlar</h3>
                        <p className="font-sans">Aşağıdaki davranışlar yasaktır:</p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li>Hizmet'i yasalara, ahlaka veya bu Koşullara aykırı şekilde kullanmak,</li>
                            <li>Başkalarına ait kimlik veya hesap bilgilerini izinsiz kullanmak,</li>
                            <li>Hizmet'in altyapısına zarar verecek otomatik istek, tersine mühendislik, kaynak kod çıkarımı veya kötü amaçlı yazılım yüklemek,</li>
                            <li>Hizmet üzerinden hukuka aykırı içerik üretmek veya yaymak,</li>
                            <li>Üçüncü kişilerin telif, marka veya kişilik haklarını ihlal etmek,</li>
                            <li>Toplantı katılımcılarının açık rızası olmadan kayıt yapmak.</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">6. Üçüncü Taraf Hizmetler</h3>
                        <p className="font-sans">
                            Hizmet, Google (OAuth, Takvim), OpenAI (özetleme ve çıkarım), ElevenLabs (konuşma-metin dönüşümü), Supabase (veri tabanı ve depolama) ve isteğe bağlı olarak Slack gibi üçüncü taraf servisleri kullanır. Bu servislerin kendi kullanım koşulları ve gizlilik politikaları geçerlidir; bu servislerin kesintilerinden, hatalarından veya politika değişikliklerinden Syncrun sorumlu tutulamaz.
                        </p>
                        <p className="mt-2 font-sans">
                            <strong>Google API Kullanımı:</strong> Syncrun, Google API'lerinden elde edilen verilerin kullanımı ve aktarımı konusunda <a href="https://developers.google.com/terms/api-services-user-data-policy" target="_blank" rel="noopener noreferrer" className="text-accent hover:text-accent-hover">Google API Services User Data Policy</a>'ye, Limited Use şartları dâhil, uymayı taahhüt eder. Google Workspace API'lerinden elde edilen verileri genel amaçlı yapay zekâ veya makine öğrenmesi modellerinin geliştirilmesi, iyileştirilmesi veya eğitilmesinde kullanmaz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">7. Yapay Zekâ Çıktıları</h3>
                        <p className="font-sans">
                            Transkripsiyon, özet, karar/aksiyon çıkarımı ve toplantı öncesi bilgilendirmeler yapay zekâ modelleriyle üretilir. Çıktılar <strong>hatalı, eksik veya yanıltıcı</strong> olabilir. Kullanıcı, bu çıktıların doğruluğunu kullanmadan önce kontrol etmekle yükümlüdür. Syncrun, yapay zekâ çıktılarına dayanılarak alınan kararlardan, yapılan eylemlerden veya doğacak zararlardan sorumlu değildir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">8. Ücretlendirme ve Abonelik</h3>
                        <p className="font-sans">
                            Hizmet'in ücretli planları için fiyatlandırma, fatura döngüsü ve iade koşulları satın alma ekranında belirtilir. Aboneliğinizi panel üzerinden istediğiniz zaman iptal edebilirsiniz; iptal mevcut dönemin sonunda etkili olur. Yasal olarak zorunlu olan durumlar dışında ödenmiş ücretler iade edilmez.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">9. Fikri Mülkiyet</h3>
                        <p className="font-sans">
                            Syncrun markası, logosu, yazılımı, arayüz tasarımı ve dokümantasyonu Syncrun'a aittir; Türkiye ve uluslararası fikri mülkiyet mevzuatıyla korunur. Hizmet'e yüklediğiniz veya Hizmet üzerinden ürettiğiniz toplantı içerikleri, notlar ve kullanıcı içeriği size aittir. Hizmet'i sunabilmek için bu içerikleri işleme, depolama ve işleme amacıyla üçüncü taraf servis sağlayıcılarına aktarma konusunda Syncrun'a sınırlı, gayri-münhasır ve geri alınabilir bir lisans verirsiniz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">10. Sorumluluğun Sınırlandırılması</h3>
                        <p className="font-sans">
                            Yürürlükteki mevzuatın izin verdiği azami ölçüde, Syncrun; dolaylı, arızi, özel, cezai veya neticesel zararlardan (gelir kaybı, veri kaybı, iş kesintisi dâhil) sorumlu değildir. Syncrun şu anda PoC/beta aşamasında ücretsiz olarak sunulmakta olup, bu süreçte Syncrun'ın toplam sorumluluğu kullanıcı başına <strong>1.000 (bin) Türk Lirası</strong> ile sınırlıdır. İleride ücretli plana geçilmesi hâlinde, ilgili olayın gerçekleşmesinden önceki 12 ay içinde Kullanıcı'nın Syncrun'a ödediği toplam ücret üst sınır olarak uygulanır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">11. Hizmetin Askıya Alınması ve Hesabın Kapatılması</h3>
                        <p className="font-sans">
                            Bu Koşullara aykırılık, ödeme aksaklığı, güvenlik tehdidi veya yasal zorunluluk hâllerinde Syncrun, hesabınızı önceden bildirimsiz olarak askıya alabilir veya kapatabilir. Hesabınızı kapatmanız hâlinde, KVKK ve ilgili mevzuat uyarınca saklanması zorunlu veriler dışındaki kişisel verileriniz Gizlilik Politikası'nda belirtilen süreler içinde silinir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">12. Değişiklikler</h3>
                        <p className="font-sans">
                            Bu Koşulları zaman zaman güncelleyebiliriz. Önemli değişiklikleri kayıtlı e-posta adresinize bildirir veya Hizmet içinde duyururuz. Güncellemenin yürürlüğe girmesinden sonra Hizmet'i kullanmaya devam etmeniz, güncellenmiş Koşulları kabul ettiğiniz anlamına gelir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">13. Uygulanacak Hukuk ve Yetkili Mahkeme</h3>
                        <p className="font-sans">
                            Bu Koşullar Türkiye Cumhuriyeti hukukuna tabidir. Bu Koşullardan doğacak uyuşmazlıklarda <strong>İstanbul Mahkemeleri ve İcra Daireleri</strong> yetkilidir. Tüketici sıfatına sahip Kullanıcılar için 6502 sayılı Tüketicinin Korunması Hakkında Kanun'da öngörülen yetkili merciler saklıdır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">14. İletişim</h3>
                        <p className="font-sans">
                            <strong>Hizmet Sağlayıcılar — Eşit Ortak Veri Sorumluları:</strong><br />
                            &bull; Faik İhsan Südüpak<br />
                            &bull; Kerem Yoldaş<br />
                            <strong>Ortak iletişim e-posta:</strong> support@syncrun.net<br />
                            <strong>KEP:</strong> Yakında eklenecektir<br />
                            <strong>Web:</strong> https://syncrun.net
                        </p>
                        <p className="mt-2 font-sans text-sm text-text-tertiary">
                            Veri sorumluları eşit oranda ve müteselsil olarak sorumludur. KVKK ve ilgili mevzuat kapsamındaki başvurularınız için yukarıdaki iletişim kanallarını kullanabilirsiniz.
                        </p>
                    </div>
                </div>
            </div>
        </div>
    )
}

export default TermsOfService
